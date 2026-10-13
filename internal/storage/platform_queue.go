package storage

import (
	"encoding/json"
	"media-harbor/internal/domain"
)

// PlatformRuns 只保存当前执行权，不复制任务或媒体记录。
// 任务状态、入队时间与来源批次仍由原 tasks 表维护。
func (s *Store) PlatformRuns() ([]domain.PlatformRun, error) {
	return list[domain.PlatformRun](s.db, "SELECT payload FROM platform_runs ORDER BY platform")
}

// SavePlatformRun 在启动批次前持久化，零配置 ID 表示该平台已释放。
// 不使用外键或触发器，服务层在任务与进程均结束后显式释放执行权。
func (s *Store) SavePlatformRun(run domain.PlatformRun) error {
	if run.SubscriptionID == 0 {
		_, err := s.db.Exec("DELETE FROM platform_runs WHERE platform=?", run.Platform)
		return err
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO platform_runs(platform,payload) VALUES(?,?) ON CONFLICT(platform) DO UPDATE SET payload=excluded.payload", run.Platform, raw)
	return err
}
