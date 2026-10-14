package storage

import (
	"media-harbor/internal/domain"
	"time"
)

// SubscriptionTasks 利用订阅索引读取目标队列，不解码其他订阅的历史。
func (s *Store) SubscriptionTasks(id int64) ([]domain.Task, error) {
	return listArgs[domain.Task](s.db, "SELECT payload FROM tasks WHERE subscription_id=? ORDER BY id DESC", id)
}

// CancelSubscriptionTasks 一次提交取消意图，不逐条等待 SQLite 写事务。
// 内容与任务使用相同的活动集合，已结束记录和文件位置保持原样。
// 返回本次取消的 ID，服务层只对这些运行上下文发出停止信号。
func (s *Store) CancelSubscriptionTasks(id int64) ([]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 内容先随活动任务更新；后续任务写入失败时仍在同一事务内回滚。
	_, err = tx.Exec(`UPDATE content_items SET payload=json_set(payload,'$.status','cancelled')
WHERE id IN (SELECT content_id FROM tasks WHERE subscription_id=? AND status IN ('queued','running') AND content_id<>0)`, id)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(`UPDATE tasks SET status='cancelled',
payload=json_set(payload,'$.status','cancelled','$.finished_at',?)
WHERE subscription_id=? AND status IN ('queued','running') RETURNING id,content_id`, time.Now().Format(time.RFC3339Nano), id)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	contents := map[int64]bool{}
	// RETURNING 只传递受影响的主键，无需解码整个历史任务快照。
	for rows.Next() {
		var taskID, cid int64
		if err = rows.Scan(&taskID, &cid); err != nil {
			break
		}
		ids = append(ids, taskID)
		if cid != 0 {
			contents[cid] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// 取消同批 C 层资源，再聚合 B；已完成的资源保留完成状态。
	for cid := range contents {
		if err = s.syncResources(tx, cid, "cancelled", nil, nil, ""); err != nil {
			return nil, err
		}
	}
	// 释放 Rows 后才能提交单连接事务，提交成功才向服务层确认结果。
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
