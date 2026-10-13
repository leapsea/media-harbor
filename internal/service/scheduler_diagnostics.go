package service

import (
	"context"
	"log/slog"
	"media-harbor/internal/config"
	"time"
)

// schedulerPhase 是不可变快照；锁本身卡住时，诊断协程仍能读取最后阶段。
type schedulerPhase struct {
	Name    string
	Started time.Time
}

func (a *App) markSchedulerPhase(name string) {
	a.schedulerPhase.Store(&schedulerPhase{Name: name, Started: time.Now()})
}

// watchScheduler 只记录长时间未结束的阶段，不取消任务或改变调度策略。
// 写到服务标准输出，可通过 docker logs 查阅，不依赖可能被阻塞的订阅日志锁。
func (a *App) watchScheduler(ctx context.Context) {
	ticker := time.NewTicker(config.SchedulerDiagnosticInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			phase := a.schedulerPhase.Load()
			if phase != nil && now.Sub(phase.Started) >= config.SchedulerDiagnosticInterval {
				slog.Warn("调度阶段长时间未结束", "stage", phase.Name, "elapsed", now.Sub(phase.Started).Round(time.Second))
			}
		}
	}
}
