package service

import (
	"fmt"
	"log/slog"
	"media-harbor/internal/domain"
	"slices"
	"sort"
	"time"
)

// runningTaskInfo 在进程实际退出前保留归属，包括已经取消或删除的任务。
type runningTaskInfo struct {
	Platform       string
	SubscriptionID int64
	CookieRevision int64
	UsesCookie     bool
	Lane           string
}

// queuedAt 兼容已有任务；显式重试使用新时间，不因旧 ID 插到队列前面。
func queuedAt(task domain.Task) time.Time {
	if !task.QueuedAt.IsZero() {
		return task.QueuedAt
	}
	return task.CreatedAt
}

// belongsToPlatformRun 允许当前扫描继续生产下载，后续扫描则作为下一批排队。
// 仅按配置 ID 放行会让周期扫描不断插队，因此同时检查批次来源和入队时刻。
func belongsToPlatformRun(task domain.Task, run domain.PlatformRun) bool {
	return run.SubscriptionID != 0 && task.SubscriptionID == run.SubscriptionID &&
		(!queuedAt(task).After(run.StartedAt) || (task.SourceTaskID != 0 && (task.SourceTaskID == run.ScanTaskID || slices.Contains(run.ScanTaskIDs, task.SourceTaskID))))
}

func samePlatformRun(a, b domain.PlatformRun) bool {
	// 恢复后的时间没有单调时钟部分，用领域时间相等判断批次是否变化。
	return a.SubscriptionID == b.SubscriptionID && a.ScanTaskID == b.ScanTaskID && slices.Equal(a.ScanTaskIDs, b.ScanTaskIDs) && a.StartedAt.Equal(b.StartedAt)
}

// platformQueueLocked 将各平台的队首配置作为唯一执行者，调用方持有应用锁。
// 执行权持久化；扫描完成后的下载间隙、服务重启和进程停止阶段均保持批次连续。
func (a *App) platformQueueLocked(tasks []domain.Task, subs map[int64]domain.Subscription, allowed map[int64]bool, limit int) (map[string]domain.PlatformRun, error) {
	saved, err := a.Repo.PlatformRuns()
	if err != nil {
		return nil, err
	}
	previous, runs := map[string]domain.PlatformRun{}, map[string]domain.PlatformRun{}
	// 保留持久快照用于差异提交，只在批次切换时写入数据库。
	for _, run := range saved {
		previous[run.Platform], runs[run.Platform] = run, run
	}
	// 活动进程才是真正的占用；数据库已取消时仍等待其退出。
	physical := map[string]int{}
	for id, info := range a.activeInfo {
		if a.active[id] == nil {
			continue
		}
		physical[info.Platform]++
		if runs[info.Platform].SubscriptionID == 0 {
			runs[info.Platform] = domain.PlatformRun{Platform: info.Platform, SubscriptionID: info.SubscriptionID, StartedAt: time.Now()}
		}
	}
	eligible := func(task domain.Task) bool {
		// 已结束任务不参与平台占用，失败后的再次执行必须显式重试。
		_, exists := subs[task.SubscriptionID]
		return exists && task.Status == "queued" && (task.ContentID == 0 || limit == 0 || allowed[task.ID])
	}
	// 调试范围外没有消费者的积压不能阻止平台交接。
	for platform, run := range runs {
		pending := false
		for _, task := range tasks {
			if eligible(task) && belongsToPlatformRun(task, run) {
				pending = true
				break
			}
		}
		if !pending && physical[platform] == 0 {
			delete(runs, platform)
		}
	}
	// 排序仅修改本轮读取的快照；后续认领复用同一顺序，避免重复排序。
	ordered := tasks
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := queuedAt(ordered[i]), queuedAt(ordered[j])
		if left.Equal(right) {
			return ordered[i].ID < ordered[j].ID
		}
		return left.Before(right)
	})
	// 两个平台分别取得队首，当前批次未结束的平台不参与下一次选择。
	for _, task := range ordered {
		if !eligible(task) {
			continue
		}
		platform := subs[task.SubscriptionID].Platform
		if runs[platform].SubscriptionID != 0 {
			continue
		}
		// 到达队首后固定批次边界，新来的定时扫描仍进入后续队列。
		scanID := task.SourceTaskID
		if task.ContentID == 0 {
			scanID = task.ID
		}
		run := domain.PlatformRun{Platform: platform, SubscriptionID: task.SubscriptionID, ScanTaskID: scanID, StartedAt: time.Now()}
		// 同配置可能同时有旧下载和已排队扫描，两者的子任务都必须能够继续消费。
		for _, pending := range ordered {
			if !eligible(pending) || pending.SubscriptionID != task.SubscriptionID || queuedAt(pending).After(run.StartedAt) {
				continue
			}
			id := pending.SourceTaskID
			if pending.ContentID == 0 {
				id = pending.ID
			}
			if id != 0 && !slices.Contains(run.ScanTaskIDs, id) {
				run.ScanTaskIDs = append(run.ScanTaskIDs, id)
			}
		}
		slices.Sort(run.ScanTaskIDs)
		runs[platform] = run
	}
	// 先写入平台归属再认领任务，重启后不会被其他博主的旧队列抢占。
	for platform, old := range previous {
		// 取消与删除后的进程占用已在前面核对，此时才允许清除执行权。
		if _, exists := runs[platform]; !exists {
			if err = a.Repo.SavePlatformRun(domain.PlatformRun{Platform: platform}); err != nil {
				return nil, err
			}
			slog.Info("平台批次结束", "platform", platform, "subscription_id", old.SubscriptionID)
		}
	}
	for platform, run := range runs {
		if samePlatformRun(run, previous[platform]) {
			continue
		}
		if err = a.Repo.SavePlatformRun(run); err != nil {
			return nil, err
		}
		slog.Info("平台批次开始", "platform", platform, "subscription_id", run.SubscriptionID, "scan_task_id", run.ScanTaskID)
	}
	// 每个等待配置只在阻挡者变化时写一次日志，不按每个媒体或每轮 tick 刷屏。
	waiting := map[int64]domain.PlatformRun{}
	for _, task := range ordered {
		if !eligible(task) {
			continue
		}
		sub := subs[task.SubscriptionID]
		run := runs[sub.Platform]
		if belongsToPlatformRun(task, run) {
			continue
		}
		if _, exists := waiting[sub.ID]; exists {
			continue
		}
		waiting[sub.ID] = run
		if samePlatformRun(run, a.queueWaits[sub.ID]) {
			continue
		}
		message := fmt.Sprintf("平台排队；平台 %s；当前配置 #%d；当前批次开始 %s；本任务等待扫描与下载全部收尾后按入队顺序执行", sub.Platform, run.SubscriptionID, run.StartedAt.Local().Format(time.RFC3339))
		if err := a.writeSubscriptionLog(sub, task, time.Now(), message); err != nil {
			slog.Warn("写入平台排队日志失败", "error", err)
		}
	}
	a.queueWaits = waiting
	return runs, nil
}
