// Package storage 使用 SQLite 持久化任务状态及领域快照。
package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"media-harbor/internal/domain"
	_ "modernc.org/sqlite"
	"time"
)

// Store 保持单连接，事务中的订阅和任务关联由应用显式维护。
type Store struct {
	db *sql.DB
}

// Open 不自动重建数据库，更不会删除下载文件或 Cookie。
// WAL 与单连接适合当前单实例队列。
// 初始化只创建缺失表，不进行开发阶段数据清理。
func Open(path string, defaults domain.Settings) (*Store, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	_, e = db.Exec(schema)
	if e != nil {
		db.Close()
		return nil, e
	}
	b, _ := json.Marshal(defaults)
	_, e = db.Exec("INSERT OR IGNORE INTO app_settings(id,payload) VALUES(1,?)", b)
	if e != nil {
		db.Close()
		return nil, e
	}
	// 中断的任务回到队列，下载程序使用原工作目录继续执行。
	tasks, e := s.Tasks()
	if e != nil {
		return nil, e
	}
	for _, t := range tasks {
		if t.Status == "running" {
			t.Status = "queued"
			t.Error = "服务重启，等待继续执行"
			t.FinishedAt = time.Time{}
			// 下载索引保留文件位置，只恢复本次执行状态，不触碰已完成或主动取消任务。
			if t.ContentID != 0 {
				c, err := s.Content(t.ContentID)
				if err != nil {
					db.Close()
					return nil, err
				}
				c.Status = "queued"
				c.Error = ""
				if err = s.SaveContent(&c); err != nil {
					db.Close()
					return nil, err
				}
			}
			if e = s.SaveTask(&t); e != nil {
				return nil, e
			}
		}
	}
	return s, nil
}

// Close 在服务停止后释放数据库连接。
func (s *Store) Close() error { return s.db.Close() }

// JSON 快照避免配置字段的重复转换，索引字段保留在关系列中。
const schema = `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS subscriptions(id INTEGER PRIMARY KEY,platform TEXT NOT NULL,identity TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(platform,identity));
CREATE TABLE IF NOT EXISTS content_items(id INTEGER PRIMARY KEY,subscription_id INTEGER NOT NULL,remote_id TEXT NOT NULL,payload TEXT NOT NULL,UNIQUE(subscription_id,remote_id));
CREATE TABLE IF NOT EXISTS download_history_cleared(content_id INTEGER PRIMARY KEY);
CREATE TABLE IF NOT EXISTS tasks(id INTEGER PRIMARY KEY,subscription_id INTEGER NOT NULL,content_id INTEGER NOT NULL,kind TEXT NOT NULL,status TEXT NOT NULL,payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks(status,id);
CREATE INDEX IF NOT EXISTS tasks_subscription_id ON tasks(subscription_id,id);
CREATE UNIQUE INDEX IF NOT EXISTS task_active ON tasks(subscription_id,content_id,kind) WHERE status IN ('queued','running');
CREATE TABLE IF NOT EXISTS app_settings(id INTEGER PRIMARY KEY,payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS engine_versions(name TEXT PRIMARY KEY,version TEXT NOT NULL,checked_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS platform_runs(platform TEXT PRIMARY KEY,payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS youtube_paid_blocks(channel_id TEXT PRIMARY KEY,failed_task_id INTEGER NOT NULL);
`

// list 和 one 只在存储适配器内部共享序列化逻辑。
// 查询始终关闭 Rows，释放单连接供后续事务使用。
func list[T any](db *sql.DB, q string) ([]T, error) {
	return listArgs[T](db, q)
}

func listArgs[T any](db *sql.DB, q string, args ...any) ([]T, error) {
	rows, e := db.Query(q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var raw []byte
		var v T
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// 查不到记录保留 sql.ErrNoRows，交由 API 映射状态。
func one[T any](db *sql.DB, q string, id int64) (T, error) {
	var v T
	var raw []byte
	e := db.QueryRow(q, id).Scan(&raw)
	if e == nil {
		e = json.Unmarshal(raw, &v)
	}
	return v, e
}

// 读取接口始终返回领域模型，HTTP 层无需了解 SQL。
// 按创建顺序倒序供订阅页面展示。
func (s *Store) Subscriptions() ([]domain.Subscription, error) {
	return list[domain.Subscription](s.db, "SELECT payload FROM subscriptions ORDER BY id DESC")
}

// 根据内部 ID 读取完整订阅配置。
func (s *Store) Subscription(id int64) (domain.Subscription, error) {
	return one[domain.Subscription](s.db, "SELECT payload FROM subscriptions WHERE id=?", id)
}

// Contents 返回完整下载索引，扫描去重和归档不受历史清空影响。
func (s *Store) Contents() ([]domain.Content, error) {
	return list[domain.Content](s.db, "SELECT payload FROM content_items ORDER BY id DESC")
}

// 工作任务只通过内部内容 ID 关联领域对象。
func (s *Store) Content(id int64) (domain.Content, error) {
	return one[domain.Content](s.db, "SELECT payload FROM content_items WHERE id=?", id)
}

// 调度器逆序遍历，使较早创建的任务先执行。
func (s *Store) Tasks() ([]domain.Task, error) {
	return list[domain.Task](s.db, "SELECT payload FROM tasks ORDER BY id DESC")
}

// TaskStatusCounts 直接聚合结构化状态列，仪表盘无需反序列化全部任务。
func (s *Store) TaskStatusCounts() (map[string]int, error) {
	rows, err := s.db.Query("SELECT status, COUNT(*) FROM tasks GROUP BY status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// 运行完成前重新读取状态，尊重用户取消意图。
func (s *Store) Task(id int64) (domain.Task, error) {
	return one[domain.Task](s.db, "SELECT payload FROM tasks WHERE id=?", id)
}

// 单用户设置只有一条持久记录。
func (s *Store) Settings() (domain.Settings, error) {
	// 只读取已保存的当前结构，不为缺失字段补入初始化参数。
	return one[domain.Settings](s.db, "SELECT payload FROM app_settings WHERE id=?", 1)
}

// 保存整体设置快照，不与启动环境默认值合并。
func (s *Store) SaveSettings(v domain.Settings) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("UPDATE app_settings SET payload=? WHERE id=1", b)
	return e
}

// SaveSubscription 对主页使用博主稳定 ID，详情任务使用内容地址。
// 详情中返回的稳定 ID 可能属于作者，不能占用作者主页的唯一身份。
// 新记录取得主键后，把该主键写入领域快照。
// 更新身份和 JSON 在同一事务内提交。
func (s *Store) SaveSubscription(v *domain.Subscription) error {
	identity := v.URL
	if domain.TaskMode(v.Platform, v.URL) == "profile" && v.StableID != "" {
		identity = v.StableID
	}
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if v.ID == 0 {
		r, e := tx.Exec("INSERT INTO subscriptions(platform,identity,payload) VALUES(?,?, '{}')", v.Platform, identity)
		if e != nil {
			return e
		}
		v.ID, e = r.LastInsertId()
		if e != nil {
			return e
		}
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = tx.Exec("UPDATE subscriptions SET platform=?,identity=?,payload=? WHERE id=?", v.Platform, identity, b, v.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}

// 删除订阅只删除记录，已下载和已归档的文件继续保留。
// 无外键或触发器，相关记录在这个事务里显式清理。
func (s *Store) DeleteSubscription(id int64) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range []string{"DELETE FROM download_history_cleared WHERE content_id IN (SELECT id FROM content_items WHERE subscription_id=?)", "DELETE FROM tasks WHERE subscription_id=?", "DELETE FROM content_items WHERE subscription_id=?", "DELETE FROM subscriptions WHERE id=?"} {
		if _, e = tx.Exec(q, id); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// SaveContent 更新文件位置和下载状态；内容创建由事务接口完成。
func (s *Store) SaveContent(v *domain.Content) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("UPDATE content_items SET payload=? WHERE id=?", b, v.ID)
	return e
}

// SaveTask 保持状态索引与 JSON 快照一致。
func (s *Store) SaveTask(v *domain.Task) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec("UPDATE tasks SET status=?,payload=? WHERE id=?", v.Status, b, v.ID)
	return e
}

// insertTask 在调用者的事务中写入，扫描记录和下载任务同时提交。
// 活动任务唯一索引约束 queued 和 running，历史尝试仍保留。
func insertTask(tx *sql.Tx, t *domain.Task) error {
	r, e := tx.Exec("INSERT INTO tasks(subscription_id,content_id,kind,status,payload) VALUES(?,?,?,?, '{}')", t.SubscriptionID, t.ContentID, t.Kind, t.Status)
	if e != nil {
		return e
	}
	t.ID, e = r.LastInsertId()
	if e != nil {
		return e
	}
	b, e := json.Marshal(t)
	if e != nil {
		return e
	}
	_, e = tx.Exec("UPDATE tasks SET payload=? WHERE id=?", b, t.ID)
	return e
}

// Enqueue 为手动或定时扫描创建一条待执行记录。
func (s *Store) Enqueue(sid, cid int64, kind string) (domain.Task, error) {
	t := domain.Task{SubscriptionID: sid, ContentID: cid, Kind: kind, Status: "queued", CreatedAt: time.Now()}
	t.QueuedAt = t.CreatedAt
	tx, e := s.db.Begin()
	if e != nil {
		return t, e
	}
	defer tx.Rollback()
	if e = insertTask(tx, &t); e != nil {
		return t, e
	}
	return t, tx.Commit()
}

// AddContentAndTask 依靠内容唯一约束实现增量去重，不重下已完成内容。
// 重复内容直接返回，不更改已有失败或完成状态。
// 下载失败的重试由应用层显式操作。
func (s *Store) AddContentAndTask(sid int64, v domain.Content) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	v.SubscriptionID = sid
	v.Status = "queued"
	v.Paths = []string{}
	r, e := tx.Exec("INSERT OR IGNORE INTO content_items(subscription_id,remote_id,payload) VALUES(?,?, '{}')", sid, v.RemoteID)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return nil
	}
	v.ID, e = r.LastInsertId()
	if e != nil {
		return e
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE content_items SET payload=? WHERE id=?", b, v.ID); e != nil {
		return e
	}
	// SQLite 内容编号可能复用；新内容不继承旧内容的历史清空状态。
	if _, e = tx.Exec("DELETE FROM download_history_cleared WHERE content_id=?", v.ID); e != nil {
		return e
	}
	t := domain.Task{SubscriptionID: sid, ContentID: v.ID, Kind: "download", Status: "queued", CreatedAt: time.Now()}
	t.QueuedAt, t.SourceTaskID = t.CreatedAt, v.SourceTaskID
	if e = insertTask(tx, &t); e != nil {
		return e
	}
	return tx.Commit()
}

// RecordVersion 将运行依赖的版本与应用设置分开存储。
func (s *Store) RecordVersion(name, version string) error {
	_, e := s.db.Exec("INSERT INTO engine_versions VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET version=excluded.version,checked_at=excluded.checked_at", name, version, time.Now().Format(time.RFC3339))
	return e
}

// IsMissing 供 HTTP 层区分不存在的记录。
func IsMissing(e error) bool { return errors.Is(e, sql.ErrNoRows) }
