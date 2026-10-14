package service

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"time"
)

// waitProductionAwake 在交付确认前暂停生产，当前条目及平台归属仍然保留。
// 引擎侧还会在每次 HTTP 请求前复核，覆盖分页、重试和头像解析。
func (a *App) waitProductionAwake(ctx context.Context, media string, log func(string)) error {
	var lastReport time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 读取最新持久配置，运行任务的初始快照不能决定恢复时间。
		settings, err := a.Repo.Settings()
		if err != nil {
			return err
		}
		until := settings.Sleep.Until(time.Now())
		// 继续确认同一条目，防止重启生产造成重复或跳过媒体。
		if until.IsZero() {
			if !lastReport.IsZero() {
				log("休眠结束；继续原生产队列；媒体 " + media)
			}
			return nil
		}
		if lastReport.IsZero() || time.Since(lastReport) >= config.ScanLogInterval {
			log(fmt.Sprintf("休眠中；阶段 生产暂停；媒体 %s；恢复时间 %s", media, until.Format(time.RFC3339)))
			lastReport = time.Now()
		}
		// 设置热更新在下一检查周期生效；等待不持有业务锁，取消立即响应。
		timer := time.NewTimer(min(time.Until(until), config.ScanQueueCheckInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
