package storage

import (
	"media-harbor/internal/domain"
	"time"
)

// ReinitializeSubscription 原子发布清空后的详情与新的全量批次。
// 只清理目标配置的帖子任务和下载资源，文件索引与执行账本保留溯源。
// 先取消旧工作再插入新扫描；事务失败时旧状态与详情一起回滚。
func (s *Store) ReinitializeSubscription(sid int64) (domain.Task, error) {
	t := domain.Task{SubscriptionID: sid, Kind: "full", Status: "queued", CreatedAt: time.Now(), Overwrite: true}
	t.QueuedAt = t.CreatedAt
	tx, err := s.db.Begin()
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	var exists int64
	if err = tx.QueryRow("SELECT id FROM subscriptions WHERE id=?", sid).Scan(&exists); err != nil {
		return t, err
	}
	// 历史完成记录保留状态；活动内容随取消变更，所有内容解除旧帖子关联。
	if _, err = tx.Exec(`UPDATE content_items SET payload=json_set(payload,'$.post_id',0,'$.status',
 CASE WHEN id IN (SELECT content_id FROM tasks WHERE subscription_id=? AND status IN ('queued','running'))
 THEN 'cancelled' ELSE json_extract(payload,'$.status') END) WHERE subscription_id=?`, sid, sid); err != nil {
		return t, err
	}
	if _, err = tx.Exec(`UPDATE tasks SET status='cancelled',payload=json_set(payload,'$.status','cancelled','$.finished_at',?)
 WHERE subscription_id=? AND status IN ('queued','running')`, time.Now().Format(time.RFC3339Nano), sid); err != nil {
		return t, err
	}
	// 无外键或触发器：先删子资源，再删帖子，其他配置的记录不参与清理。
	for _, query := range []string{
		"DELETE FROM download_items WHERE post_id IN (SELECT id FROM post_tasks WHERE subscription_id=?)",
		"DELETE FROM post_tasks WHERE subscription_id=?",
	} {
		if _, err = tx.Exec(query, sid); err != nil {
			return t, err
		}
	}
	if err = insertTask(tx, &t); err != nil {
		return t, err
	}
	return t, tx.Commit()
}

// QueueFullContent 为本轮发现的资源创建新执行与工作目录，已有路径只用于覆盖归位。
func (s *Store) QueueFullContent(sid int64, item domain.Content) (bool, error) {
	return s.addContentAndTask(sid, item, true)
}
