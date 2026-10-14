package storage

import (
	"database/sql"
	"encoding/json"
	"media-harbor/internal/domain"
	"slices"
	"strings"
	"time"
)

// Posts/Post 使用 B 层唯一键核对当前配置，不读取全站资源索引。
// 同一个帖子的 URL 别名在领域层归一，轮播序号不进入 B 的唯一键。
// 数据库保持单连接，读取返回后关闭 Rows，后续事务才能继续执行。
func (s *Store) Posts(sid int64) ([]domain.PostTask, error) {
	return listArgs[domain.PostTask](s.db, "SELECT payload FROM post_tasks WHERE subscription_id=? ORDER BY id", sid)
}
func (s *Store) Post(sid int64, key string) (domain.PostTask, error) {
	var p domain.PostTask
	var raw []byte
	err := s.db.QueryRow("SELECT payload FROM post_tasks WHERE subscription_id=? AND remote_id=?", sid, key).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &p)
	}
	return p, err
}
func (s *Store) DownloadItems(postID int64) ([]domain.DownloadItem, error) {
	return listArgs[domain.DownloadItem](s.db, "SELECT payload FROM download_items WHERE post_id=? ORDER BY id", postID)
}

// RegisterPost 在解析交付时确定期望清单；只有 SDK 给出总数才确认 Instagram 清单。
// 注册 B 与调整已有 C 的期望集合使用同一事务，不暴露半次配置变更。
// 尚未入队的新资源由 AddContentAndTask 创建，注册本身不创建执行尝试。
// 已有资源的状态、路径及主键继续保留，只有新启用的产物进入待执行。
func (s *Store) RegisterPost(sid int64, platform string, item domain.Content, o domain.YouTubeOptions) (domain.PostTask, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return domain.PostTask{}, err
	}
	defer tx.Rollback()
	p, err := registerPost(tx, sid, platform, item, o)
	if err != nil {
		return p, err
	}
	var raw []byte
	var existing domain.Content
	// 文件索引用于保持旧身份；B/C 是新的完整性与子任务状态来源。
	// 未命中索引说明这是新资源，后续入队事务会建立 C 和执行账本。
	lookup := tx.QueryRow("SELECT payload FROM content_items WHERE subscription_id=? AND remote_id=?", sid, item.RemoteID).Scan(&raw)
	if lookup == nil {
		if err = json.Unmarshal(raw, &existing); err != nil {
			return p, err
		}
		existing.ExpectedAssets = item.ExpectedAssets
		if err = s.attachContent(tx, &existing); err != nil {
			return p, err
		}
	} else if lookup != sql.ErrNoRows {
		return p, lookup
	}
	if err = refreshPost(tx, p.ID); err != nil {
		return p, err
	}
	if err = tx.Commit(); err != nil {
		return p, err
	}
	return s.Post(sid, p.RemoteID)
}

// registerPost 与创建 C 共用事务，B 不会在部分资源入库时提前完成。
// 新建记录先取得主键再发布 JSON 快照，整个过程未提交时对其他读取不可见。
// 解析确认资源总数以后才设置 manifest_complete，迁移推测不进入这个路径。
// YouTube 的单视频清单由已保存的期望选项确定，评论与元数据分别计数。
func registerPost(tx *sql.Tx, sid int64, platform string, item domain.Content, o domain.YouTubeOptions) (domain.PostTask, error) {
	key := domain.PostIdentity(platform, item)
	p := domain.PostTask{SubscriptionID: sid, RemoteID: key, URL: item.URL, Title: item.Title, Status: "queued"}
	var raw []byte
	err := tx.QueryRow("SELECT payload FROM post_tasks WHERE subscription_id=? AND remote_id=?", sid, key).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &p)
	} else if err == sql.ErrNoRows {
		err = nil
	}
	if err != nil {
		return p, err
	}
	// 唯一约束负责幂等身份；数据库关联由这里显式维护，不依赖触发器。
	// 同一 B 再次解析只更新清单与标题，不重新生成子资源或执行主键。
	if p.ID == 0 {
		r, err := tx.Exec("INSERT INTO post_tasks(subscription_id,remote_id,payload) VALUES(?,?, '{}')", sid, key)
		if err != nil {
			return p, err
		}
		p.ID, err = r.LastInsertId()
		if err != nil {
			return p, err
		}
	}
	if item.Title != "" {
		p.Title = item.Title
	}
	if item.URL != "" {
		p.URL = item.URL
	}
	if platform == "instagram" {
		if item.ExpectedAssets > 0 {
			p.Expected = item.ExpectedAssets
			p.ManifestComplete = true
		}
	} else {
		p.Expected = len(domain.ExpectedKinds(platform, o))
		p.ManifestComplete = true
	}
	p.Kinds = domain.ExpectedKinds(platform, o)
	p.UpdatedAt = time.Now()
	b, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	_, err = tx.Exec("UPDATE post_tasks SET payload=? WHERE id=?", b, p.ID)
	return p, err
}

// attachContent 保留原内容/执行 ID，创建独立 C 记录；任务表只作为执行尝试与队列账本。
// A 和设置在该事务内读取，C 的期望产物使用同一份配置视图。
// 原内容记录继续保存文件索引，PostID 让旧接口也能定位真实 B。
// 迁移只有首次关联会创建 C，重启和重复扫描使用唯一约束保持幂等。
func (s *Store) attachContent(tx *sql.Tx, item *domain.Content) error {
	var raw []byte
	var sub domain.Subscription
	var settings domain.Settings
	if err := tx.QueryRow("SELECT payload FROM subscriptions WHERE id=?", item.SubscriptionID).Scan(&raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &sub); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT payload FROM app_settings WHERE id=1").Scan(&raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return err
	}
	o := settings.YouTube
	if sub.YouTube != nil {
		o = *sub.YouTube
	}
	p, err := registerPost(tx, sub.ID, sub.Platform, *item, o)
	if err != nil {
		return err
	}
	// 已有关联代表新增产物，不能用原视频的 completed 初始化这个新 C。
	// 迁移首次关联则保留旧终态，Instagram 的未知清单仍阻止 B 提前完成。
	wasAttached := item.PostID != 0
	item.PostID = p.ID
	// 新的期望清单不删除旧资源，只调整 required 标记。
	// 已下载文件与历史记录保持可追溯；停用子项不再参与 B 的完成计数。
	// required 的调整和 B 的期望数量在本次事务结束时一并发布。
	if _, err = tx.Exec("UPDATE download_items SET payload=json_set(payload,'$.required',json('false')) WHERE content_id=?", item.ID); err != nil {
		return err
	}
	for _, kind := range domain.ExpectedKinds(sub.Platform, o) {
		d := domain.DownloadItem{Required: true, PostID: p.ID, ContentID: item.ID, Kind: kind, Status: item.Status, Paths: item.Paths, UpdatedAt: time.Now()}
		if wasAttached && d.Status == "completed" {
			d.Status = "queued"
		}
		r, err := tx.Exec("INSERT OR IGNORE INTO download_items(post_id,content_id,kind,payload) VALUES(?,?,?, '{}')", p.ID, item.ID, kind)
		if err != nil {
			return err
		}
		added, err := r.RowsAffected()
		if err != nil {
			return err
		}
		// 命中已有 C 时只恢复期望标记，不覆盖它的进度和完成结果。
		// 新增 C 的主键、初始状态与所属 B 在下方一起写入快照。
		if added == 0 {
			if _, err = tx.Exec("UPDATE download_items SET payload=json_set(payload,'$.required',json('true')) WHERE post_id=? AND content_id=? AND kind=?", p.ID, item.ID, kind); err != nil {
				return err
			}
			continue
		}
		d.ID, err = r.LastInsertId()
		if err != nil {
			return err
		}
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE download_items SET payload=? WHERE id=?", b, d.ID); err != nil {
			return err
		}
	}
	b, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE content_items SET payload=? WHERE id=?", b, item.ID); err != nil {
		return err
	}
	return refreshPost(tx, p.ID)
}

// syncResources 将实际执行阶段写入 C；失败保留已经完成的子项，重试不会撤销产物。
// 执行账本兼容旧控制接口，状态变化必须在同一事务传递到 C 和 B。
// 先关闭资源读取的 Rows 再更新，避免单连接事务等待自身持有的读取游标。
// 文件位置由现有分类结果关联，不另建一套扩展名或评论检测规则。
func (s *Store) syncResources(tx *sql.Tx, cid int64, status string, progress []domain.TransferProgress, paths []string, message string) error {
	rows, err := tx.Query("SELECT payload FROM download_items WHERE content_id=?", cid)
	if err != nil {
		return err
	}
	items := []domain.DownloadItem{}
	for rows.Next() {
		var raw []byte
		var d domain.DownloadItem
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &d)
		}
		if err != nil {
			break
		}
		items = append(items, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var content domain.Content
	if paths != nil {
		var raw []byte
		if err = tx.QueryRow("SELECT payload FROM content_items WHERE id=?", cid).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &content); err != nil {
			return err
		}
	}
	posts := map[int64]bool{}
	for _, d := range items {
		if paths != nil {
			d.Paths = paths
			if len(content.Files) > 0 && d.Kind != "media" {
				d.Paths = []string{}
				for _, file := range content.Files {
					match := slices.Contains(file.Tags, d.Kind)
					if d.Kind == "audio" {
						match = match && !strings.Contains(file.Path, ".extracted.")
					}
					if d.Kind == "extracted" {
						match = slices.Contains(file.Tags, "audio") && strings.Contains(file.Path, ".extracted.")
					}
					if match {
						d.Paths = append(d.Paths, file.Path)
					}
				}
			}
		}
		// SDK 的阶段事件是子资源进度来源，评论与元数据可共享同一实际文件。
		// 平台明确没有某可选资源时记录 unavailable，不声称生成了不存在的文件。
		var observed *domain.TransferProgress
		for i := range progress {
			if progress[i].Kind == d.Kind || (d.Kind == "comments" && progress[i].Kind == "metadata") {
				observed = &progress[i]
				break
			}
		}
		if observed != nil && observed.Status != "" {
			d.Status, d.Error = observed.Status, observed.Error
			d.Progress = *observed
			d.Progress.Kind = d.Kind
			if d.Status == "skipped" {
				d.Status = "completed"
				d.Outcome = "unavailable"
			}
		} else if status == "completed" {
			d.Status = "completed"
			d.Error = ""
		} else if d.Status != "completed" {
			// 多阶段视频认领只改变执行记录；未开始的子任务仍待执行。
			if status != "running" || d.Kind == "media" {
				d.Status = status
				d.Error = message
			}
		}
		// 进程取消/重启时未结束的阶段必须跟随持久执行状态，不遗留 running。
		if (status == "cancelled" || status == "queued" || status == "failed" || status == "skipped") && d.Status != "completed" {
			d.Status = status
			d.Error = message
		}
		d.Progress.Kind = d.Kind
		d.Progress.Status = d.Status
		d.UpdatedAt = time.Now()
		b, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE download_items SET payload=? WHERE id=?", b, d.ID); err != nil {
			return err
		}
		posts[d.PostID] = true
	}
	for id := range posts {
		if err = refreshPost(tx, id); err != nil {
			return err
		}
	}
	return nil
}

// refreshPost 只聚合已确定的期望子任务，不用“已发现的几个都完成”代替完整清单。
// required 与期望类型同时筛选，配置停用的旧产物不能增加完成数量。
// 资源数量必须等于已确认的期望数量，未知轮播总数不允许发布完成。
// 运行状态优先于等待与失败，最后一个阶段还需执行账本确认文件归位。
func refreshPost(tx *sql.Tx, id int64) error {
	var p domain.PostTask
	var raw []byte
	if err := tx.QueryRow("SELECT payload FROM post_tasks WHERE id=?", id).Scan(&raw); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT payload FROM download_items WHERE post_id=?", id)
	if err != nil {
		return err
	}
	count := 0
	states := map[string]int{}
	p.Completed = 0
	p.Error = ""
	for rows.Next() {
		var d domain.DownloadItem
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &d)
		}
		if err != nil {
			break
		}
		if !d.Required || !slices.Contains(p.Kinds, d.Kind) {
			continue
		}
		count++
		states[d.Status]++
		if d.Status == "completed" {
			p.Completed++
		}
		if d.Error != "" {
			p.Error = d.Error
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	// 最后一项传输结束还需文件归位与执行提交，B 不提前发布完成。
	// 每个内容只使用最新一次执行状态，旧失败尝试不能阻止成功重试完成。
	// 取消及失败保留其终态，不能因为所有传输事件到达而被覆盖为成功。
	rows, err = tx.Query("SELECT t.status FROM tasks t WHERE t.content_id IN (SELECT content_id FROM download_items WHERE post_id=?) AND t.id=(SELECT MAX(latest.id) FROM tasks latest WHERE latest.content_id=t.content_id)", id)
	if err != nil {
		return err
	}
	execution := map[string]int{}
	for rows.Next() {
		var state string
		if err = rows.Scan(&state); err != nil {
			break
		}
		execution[state]++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	p.Status = "queued"
	if p.ManifestComplete && count == p.Expected && p.Completed == p.Expected && execution["queued"] == 0 && execution["running"] == 0 && execution["failed"] == 0 && execution["cancelled"] == 0 && execution["skipped"] == 0 {
		p.Status = "completed"
		p.Error = ""
	} else {
		for _, state := range []string{"running", "queued", "failed", "cancelled", "skipped"} {
			if states[state] > 0 || execution[state] > 0 {
				p.Status = state
				break
			}
		}
	}
	p.UpdatedAt = time.Now()
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE post_tasks SET payload=? WHERE id=?", b, id)
	return err
}

// migratePosts 幂等地将旧文件索引关联到 B/C，不重建旧任务或移动文件。
// 旧 Instagram 缺少总数，迁移后清单仍待确认，后续一次解析即可补齐。
// 所有旧内容的关联在一个事务内提交，异常时不留下半个升级批次。
// 已关联的内容不会重复创建 C，运行中的旧执行由启动恢复逻辑接着处理。
// 文件不参与复制和迁移，旧路径、归档状态与内容身份全部保留。
func (s *Store) migratePosts() error {
	items, err := s.Contents()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applied int
	if err = tx.QueryRow("SELECT COUNT(*) FROM task_model_migrations WHERE name='post-resource-progress-v1'").Scan(&applied); err != nil {
		return err
	}
	for i := range items {
		if items[i].PostID == 0 {
			if err = s.attachContent(tx, &items[i]); err != nil {
				return err
			}
		}
		// 升级一次回放旧阶段记录，保存 completed/skipped、实际字节及可选产物不存在的结果。
		// 不仅依据父任务的 completed 初始化所有子项，避免详情丢失“无需下载”语义。
		if applied == 0 {
			var raw []byte
			var task domain.Task
			readErr := tx.QueryRow("SELECT payload FROM tasks WHERE content_id=? ORDER BY id DESC LIMIT 1", items[i].ID).Scan(&raw)
			if readErr == sql.ErrNoRows {
				continue
			}
			if readErr != nil {
				return readErr
			}
			if err = json.Unmarshal(raw, &task); err != nil {
				return err
			}
			if err = s.syncResources(tx, items[i].ID, task.Status, task.Progress, items[i].Paths, task.Error); err != nil {
				return err
			}
		}
	}
	// 空数据库无需标记历史迁移；首次真实旧索引升级完成后才发布版本标记。
	if applied == 0 && len(items) > 0 {
		if _, err = tx.Exec("INSERT INTO task_model_migrations(name) VALUES('post-resource-progress-v1')"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PostDownloadRows 一次关联 B/C 与文件索引、最新执行记录；调试范围仍按媒体条目限制。
// 不从执行记录反推帖子，不单独读取全站历史，也不在持有 Rows 时发起嵌套查询。
// 调试选择按内容归属限制，一个 YouTube 视频的全部期望子项保持可见。
// B 主键决定展示分组，C 主键决定子项身份，不依赖 URL 字符串的临时切分。
// 历史尝试只取最新一条，用于取消/重试操作和本次执行时间排序。
func (s *Store) PostDownloadRows(sid int64, limit int) ([]domain.PostDownloadRow, error) {
	query := `WITH selected AS (
 SELECT content_id FROM tasks WHERE subscription_id=? AND content_id<>0 GROUP BY content_id ORDER BY MIN(id)`
	args := []any{sid}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query += `) SELECT p.payload,d.payload,c.payload,t.payload
 FROM post_tasks p JOIN download_items d ON d.post_id=p.id
 JOIN content_items c ON c.id=d.content_id
 JOIN selected selected ON selected.content_id=d.content_id
 JOIN tasks t ON t.id=(SELECT MAX(last.id) FROM tasks last WHERE last.content_id=d.content_id)
 WHERE p.subscription_id=? AND json_extract(d.payload,'$.required')=1 ORDER BY p.id,d.id`
	args = append(args, sid)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PostDownloadRow{}
	for rows.Next() {
		var a, b, c, d []byte
		var row domain.PostDownloadRow
		if err = rows.Scan(&a, &b, &c, &d); err != nil {
			return nil, err
		}
		for _, pair := range []struct {
			raw    []byte
			target any
		}{{a, &row.Post}, {b, &row.Download}, {c, &row.Content}, {d, &row.Task}} {
			if err = json.Unmarshal(pair.raw, pair.target); err != nil {
				return nil, err
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
