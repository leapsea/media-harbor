package storage

import (
	"database/sql"
	"errors"
)

// PaidFailure 按频道读取持续跳过状态，未被阻断的频道返回零。
func (s *Store) PaidFailure(channelID string) (int64, error) {
	var id int64
	err := s.db.QueryRow("SELECT failed_task_id FROM youtube_paid_blocks WHERE channel_id=?", channelID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// BlockPaidVideos 保留首次失败的任务，后续失败不覆盖原因来源。
func (s *Store) BlockPaidVideos(channelID string, taskID int64) error {
	_, err := s.db.Exec("INSERT INTO youtube_paid_blocks VALUES(?,?) ON CONFLICT(channel_id) DO NOTHING", channelID, taskID)
	return err
}

// AllowPaidVideos 只由用户显式重试调用，普通扫描和服务重启不解除策略。
func (s *Store) AllowPaidVideos(channelID string) error {
	_, err := s.db.Exec("DELETE FROM youtube_paid_blocks WHERE channel_id=?", channelID)
	return err
}
