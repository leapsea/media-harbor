package storage

import (
	"database/sql"
	"encoding/json"
	"media-harbor/internal/domain"
	"time"
)

// TaskGroupStats 只聚合管理表格需要的字段，不在 Go 中解码完整任务 JSON 和传输进度数组。
func (s *Store) TaskGroupStats(limit int) ([]domain.TaskGroupStats, error) {
	const query = `WITH task_values AS (
 SELECT id, subscription_id, content_id, status,
        COALESCE(json_extract(payload,'$.error'),'') AS error,
        max(COALESCE(json_extract(payload,'$.created_at'),''),
			COALESCE(json_extract(payload,'$.queued_at'),''),
            COALESCE(json_extract(payload,'$.started_at'),''),
            COALESCE(json_extract(payload,'$.finished_at'),'')) AS changed_at
 FROM tasks
), ranked_downloads AS (
 SELECT *, ROW_NUMBER() OVER (PARTITION BY subscription_id ORDER BY id ASC) AS row_num
 FROM task_values WHERE content_id<>0
), selected_downloads AS (
 SELECT id, subscription_id, content_id, status, error, changed_at
 FROM ranked_downloads WHERE ?=0 OR row_num<=?
), ranked_scans AS (
 SELECT *, ROW_NUMBER() OVER (PARTITION BY subscription_id ORDER BY id DESC) AS row_num
 FROM task_values WHERE content_id=0
), selected_scans AS (
 SELECT id, subscription_id, content_id, status, error, changed_at
 FROM ranked_scans WHERE row_num=1
), selected AS (
 SELECT * FROM selected_downloads UNION ALL SELECT * FROM selected_scans
), all_counts AS (
 SELECT subscription_id,
        SUM(status='queued') AS queued,
        SUM(status='running') AS running,
        SUM(status='failed') AS failed,
        SUM(status='cancelled') AS cancelled,
        MAX(changed_at) AS updated_at
 FROM selected GROUP BY subscription_id
), download_counts AS (
 SELECT subscription_id, COUNT(*) AS total,
        SUM(status='queued') AS queued,
        SUM(status='running') AS running,
        SUM(status='completed') AS completed,
        SUM(status='failed') AS failed,
        SUM(status='cancelled') AS cancelled,
        SUM(status='skipped') AS skipped
 FROM selected WHERE content_id<>0 GROUP BY subscription_id
), last_download_error AS (
 SELECT selected.subscription_id, selected.error
 FROM selected JOIN (
   SELECT subscription_id, MAX(id) AS id FROM selected
   WHERE content_id<>0 AND error<>'' GROUP BY subscription_id
 ) latest ON latest.subscription_id=selected.subscription_id AND latest.id=selected.id
), latest_scan AS (
 SELECT subscription_id, status, error FROM selected WHERE content_id=0
)
SELECT all_counts.subscription_id,
       COALESCE(download_counts.total,0),
       COALESCE(download_counts.queued,0),
       COALESCE(download_counts.running,0),
       COALESCE(download_counts.completed,0),
       COALESCE(download_counts.failed,0),
       COALESCE(download_counts.cancelled,0),
       COALESCE(download_counts.skipped,0),
       COALESCE(latest_scan.status,''),
       all_counts.updated_at,
       COALESCE(NULLIF(latest_scan.error,''),last_download_error.error,''),
       all_counts.queued, all_counts.running, all_counts.failed, all_counts.cancelled
FROM all_counts
LEFT JOIN download_counts USING(subscription_id)
LEFT JOIN latest_scan USING(subscription_id)
LEFT JOIN last_download_error USING(subscription_id)
ORDER BY all_counts.updated_at DESC`

	rows, err := s.db.Query(query, limit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.TaskGroupStats{}
	for rows.Next() {
		var item domain.TaskGroupStats
		var updatedAt string
		var queued, running, completed, failed, cancelled, skipped int
		if err := rows.Scan(&item.SubscriptionID, &item.Total,
			&queued, &running, &completed, &failed, &cancelled, &skipped,
			&item.ScanStatus, &updatedAt, &item.Error,
			&item.Queued, &item.Running, &item.Failed, &item.Cancelled); err != nil {
			return nil, err
		}
		item.Counts = map[string]int{"queued": queued, "running": running, "completed": completed, "failed": failed, "cancelled": cancelled, "skipped": skipped}
		if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
			item.UpdatedAt = parsed
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// TaskContentRows 单次查询指定订阅的任务和关联内容。
// 测试模式保留 selectedTasks 的语义：最早的 N 条媒体任务和最新扫描。
func (s *Store) TaskContentRows(subscriptionID int64, limit int) ([]domain.TaskContentRow, error) {
	const query = `WITH scoped_tasks AS (
 SELECT id, subscription_id, content_id, payload FROM tasks WHERE subscription_id=?
), ranked_downloads AS (
 SELECT *, ROW_NUMBER() OVER (ORDER BY id ASC) AS row_num
 FROM scoped_tasks WHERE content_id<>0
), selected_downloads AS (
 SELECT * FROM ranked_downloads WHERE ?=0 OR row_num<=?
), ranked_scans AS (
 SELECT *, ROW_NUMBER() OVER (ORDER BY id DESC) AS row_num
 FROM scoped_tasks WHERE content_id=0
), selected_scans AS (
 SELECT * FROM ranked_scans WHERE row_num=1
), selected AS (
 SELECT id, subscription_id, content_id, payload FROM selected_downloads
 UNION ALL SELECT id, subscription_id, content_id, payload FROM selected_scans
)
SELECT selected.payload, COALESCE(content_items.payload,'{}')
FROM selected LEFT JOIN content_items ON content_items.id=selected.content_id
ORDER BY selected.id ASC`
	rows, err := s.db.Query(query, subscriptionID, limit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.TaskContentRow{}
	for rows.Next() {
		var taskJSON, contentJSON []byte
		var item domain.TaskContentRow
		if err := rows.Scan(&taskJSON, &contentJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(taskJSON, &item.Task); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(contentJSON, &item.Content); err != nil {
			return nil, err
		}
		if item.Task.ContentID != 0 && item.Content.ID == 0 {
			return nil, sql.ErrNoRows
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
