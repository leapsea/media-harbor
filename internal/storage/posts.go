package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"media-harbor/internal/domain"
	"slices"
	"strings"
	"time"
)

// Posts/Post 使用帖子任务唯一键核对当前配置，不读取全站资源索引。
// 同一个帖子的 URL 别名在领域层归一，轮播序号不进入帖子任务的唯一键。
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
// 注册帖子任务与调整已有下载资源任务的期望集合使用同一事务，不暴露半次配置变更。
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
	// 文件索引保存媒体集合；帖子任务与下载资源任务是完整性与子任务状态的来源。
	// 未命中索引说明这是新资源，后续入队事务会建立下载资源任务和执行账本。
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

// registerPost 与创建下载资源任务共用事务，帖子任务不会在部分资源入库时提前完成。
// 新建记录先取得主键再发布 JSON 快照，整个过程未提交时对其他读取不可见。
// 解析确认资源总数以后才设置 manifest_complete，不以已发现条目数量推测总数。
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
	// 同一帖子任务再次解析只更新清单与标题，不重新生成子资源或执行主键。
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

// attachContent 保留原内容/执行 ID，创建独立下载资源任务记录；任务表只作为执行尝试与队列账本。
// 订阅配置和设置在该事务内读取，下载资源任务的期望产物使用同一份配置视图。
// 媒体集合记录文件索引，PostID 明确关联到帖子任务。
// 新资源生成下载资源任务，重复扫描使用唯一约束保持身份及执行状态。
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
	// 新增的下载资源任务总是待执行，不继承媒体集合或已有视频的完成状态。
	item.PostID = p.ID
	// 新的期望清单不删除旧资源，只调整 required 标记。
	// 已下载文件与历史记录保持可追溯；停用子项不再参与帖子任务的完成计数。
	// required 的调整和帖子任务的期望数量在本次事务结束时一并发布。
	if _, err = tx.Exec("UPDATE download_items SET payload=json_set(payload,'$.required',json('false')) WHERE content_id=?", item.ID); err != nil {
		return err
	}
	for _, kind := range domain.ExpectedKinds(sub.Platform, o) {
		d := domain.DownloadItem{Required: true, PostID: p.ID, ContentID: item.ID, Kind: kind, Status: "queued", Progress: domain.TransferProgress{Kind: kind, Status: "queued"}, Paths: []string{}, UpdatedAt: time.Now()}
		r, err := tx.Exec("INSERT OR IGNORE INTO download_items(post_id,content_id,kind,payload) VALUES(?,?,?, '{}')", p.ID, item.ID, kind)
		if err != nil {
			return err
		}
		added, err := r.RowsAffected()
		if err != nil {
			return err
		}
		// 命中已有下载资源任务时只恢复期望标记，不覆盖它的进度和完成结果。
		// 新增下载资源任务的主键、初始状态与所属帖子任务在下方一起写入快照。
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

// syncResources 将实际执行阶段写入下载资源任务；失败保留已经完成的子项，重试不会撤销产物。
// 执行账本记录工作单元，状态变化必须在同一事务传递到下载资源任务和帖子任务。
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
			if d.Kind != "media" {
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
	// 最后一项传输结束还需文件归位与执行提交，帖子任务不提前发布完成。
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

// PostDownloadRows 一次关联帖子任务与下载资源任务与文件索引、最新执行记录；调试范围仍按媒体条目限制。
// 不从执行记录反推帖子，不单独读取全站历史，也不在持有 Rows 时发起嵌套查询。
// 调试选择按内容归属限制，一个 YouTube 视频的全部期望子项保持可见。
// 帖子任务主键决定展示分组，下载资源任务主键决定子项身份，不依赖 URL 字符串的临时切分。
// 历史尝试只取最新一条，用于取消/重试操作和本次执行时间排序。
func (s *Store) PostDownloadRows(ctx context.Context, sid int64, limit int) ([]domain.PostDownloadRow, error) {
	// 一次聚合最新执行 ID，前十条选择保持首次发现顺序；不按下载资源任务重复查找历史尝试。
	query := `WITH selected AS (
 SELECT content_id,MAX(id) AS task_id FROM tasks WHERE subscription_id=? AND content_id<>0 GROUP BY content_id ORDER BY MIN(id)`
	args := []any{sid}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query += `) SELECT p.id,p.payload,d.payload,c.id,json_extract(c.payload,'$.media_type'),
 t.id,json_extract(t.payload,'$.created_at'),json_extract(t.payload,'$.started_at')
 FROM post_tasks p JOIN download_items d ON d.post_id=p.id
 JOIN content_items c ON c.id=d.content_id
 JOIN selected selected ON selected.content_id=d.content_id
 JOIN tasks t ON t.id=selected.task_id
 WHERE p.subscription_id=? AND json_extract(d.payload,'$.required')=1 ORDER BY p.id,d.id`
	args = append(args, sid)
	// 历史查询：selected 仅选 content_id；每个下载资源任务再通过全站 MAX(id) 查最新尝试。
	// SELECT p.payload,d.payload,c.payload,t.payload ...
	// JOIN tasks t ON t.id=(SELECT MAX(last.id) FROM tasks last WHERE last.content_id=d.content_id)
	// s.db.Query(query, args...) 在关闭弹窗后仍会排队等待并继续执行。
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.PostDownloadRow{}
	posts := map[int64]domain.PostTask{}
	for rows.Next() {
		var a, b []byte
		var postID int64
		var mediaType, created, started sql.NullString
		var row domain.PostDownloadRow
		if err = rows.Scan(&postID, &a, &b, &row.Content.ID, &mediaType, &row.Task.ID, &created, &started); err != nil {
			return nil, err
		}
		// 同一帖子任务只解码一次；文件路径、任务日志与任务进度不参与详情投影。
		post, exists := posts[postID]
		if !exists {
			if err = json.Unmarshal(a, &post); err != nil {
				return nil, err
			}
			posts[postID] = post
		}
		row.Post = post
		row.Content.MediaType = mediaType.String
		if err = json.Unmarshal(b, &row.Download); err != nil {
			return nil, err
		}
		// 时间仍使用领域的 RFC3339 精度；空字段与零值保持原 JSON 解码语义。
		for _, field := range []struct {
			raw    sql.NullString
			target *time.Time
		}{{created, &row.Task.CreatedAt}, {started, &row.Task.StartedAt}} {
			if field.raw.Valid {
				*field.target, err = time.Parse(time.RFC3339Nano, field.raw.String)
				if err != nil {
					return nil, err
				}
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
