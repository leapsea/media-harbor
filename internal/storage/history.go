package storage

import "media-harbor/internal/domain"

// DownloadHistory 是下载索引的展示投影，清空状态独立于内容和队列状态。
// 不复制文件信息或回填旧数据；没有清空标记的记录正常展示。
func (s *Store) DownloadHistory() ([]domain.Content, error) {
	return list[domain.Content](s.db, `SELECT c.payload FROM content_items c
 WHERE NOT EXISTS (SELECT 1 FROM download_history_cleared h WHERE h.content_id=c.id)
 ORDER BY c.id DESC`)
}

// DownloadHistoryPage 解码当前页的内容快照，避免历史页读取所有产物路径。
func (s *Store) DownloadHistoryPage(offset, limit int) ([]domain.Content, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		return []domain.Content{}, nil
	}
	return listArgs[domain.Content](s.db, `SELECT c.payload FROM content_items c
 WHERE NOT EXISTS (SELECT 1 FROM download_history_cleared h WHERE h.content_id=c.id)
 ORDER BY c.id DESC LIMIT ? OFFSET ?`, limit, offset)
}

// DownloadHistoryCount 只统计可见历史行，不读取 JSON 内容或触碰文件系统。
func (s *Store) DownloadHistoryCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM content_items c
 WHERE NOT EXISTS (SELECT 1 FROM download_history_cleared h WHERE h.content_id=c.id)`).Scan(&count)
	return count, err
}

// RecentDownloadHistory 在数据库中限定总览数据量，不解码其余历史记录。
func (s *Store) RecentDownloadHistory(limit int) ([]domain.Content, error) {
	if limit <= 0 {
		return []domain.Content{}, nil
	}
	return listArgs[domain.Content](s.db, `SELECT c.payload FROM content_items c
 WHERE NOT EXISTS (SELECT 1 FROM download_history_cleared h WHERE h.content_id=c.id)
 ORDER BY c.id DESC LIMIT ?`, limit)
}

// ClearDownloadHistory 原子清空当前历史投影，不修改索引、任务、归档状态或文件。
// 重复清空只返回本次移除的展示记录数量。
func (s *Store) ClearDownloadHistory() (int, error) {
	result, err := s.db.Exec("INSERT OR IGNORE INTO download_history_cleared(content_id) SELECT id FROM content_items")
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

// ShowDownloadHistory 只在新一轮下载执行时恢复历史；普通状态更新不恢复已清空记录。
func (s *Store) ShowDownloadHistory(id int64) error {
	_, err := s.db.Exec("DELETE FROM download_history_cleared WHERE content_id=?", id)
	return err
}
