package service

import (
	"log/slog"
	"media-harbor/internal/domain"
	"sort"
	"time"
)

// TaskGroup 将一个订阅的扫描和下载状态收口为一行，底层任务仍独立持久化。
// 下载统计与扫描状态分开，避免扫描成功被误认为下载完成。
type TaskGroup struct {
	SubscriptionID   int64          `json:"subscription_id"`
	Status           string         `json:"status"`
	ScanStatus       string         `json:"scan_status"`
	Counts           map[string]int `json:"counts"`
	Total            int            `json:"total"`
	Error            string         `json:"error"`
	UpdatedAt        time.Time      `json:"updated_at"`
	PlatformDisabled bool           `json:"platform_disabled"`
	BlockedReason    string         `json:"blocked_reason,omitempty"`
	SleepUntil       *time.Time     `json:"sleep_until,omitempty"`
	TestItemLimit    int            `json:"test_item_limit"`
}

// selectedTasks 保留最新扫描和按发现顺序选中的下载。
// 测试模式同时约束旧队列与重试范围，不删除之前的历史记录。
func selectedTasks(tasks []domain.Task, subscriptionID int64, limit int) []domain.Task {
	ordered := append([]domain.Task(nil), tasks...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	// 使用副本排序，调用者的原始任务快照不被改变。
	selected := []domain.Task{}
	// 下载 ID 升序对应最初发现顺序，恢复或重试不会另选十条。
	var scan *domain.Task
	downloads := 0
	for _, task := range ordered {
		if task.SubscriptionID != subscriptionID {
			continue
		}
		if task.ContentID == 0 {
			current := task
			scan = &current
			continue
		}
		if limit > 0 && downloads >= limit {
			continue
		}
		selected = append(selected, task)
		downloads++
	}
	if scan != nil {
		selected = append(selected, *scan)
	}
	return selected
}

// TaskGroups 由服务端统一生成统计，界面和批量操作使用相同范围。
func (a *App) TaskGroups() ([]TaskGroup, error) {
	settings, err := a.Repo.Settings()
	if err != nil {
		return nil, err
	}
	subs, err := a.Repo.Subscriptions()
	if err != nil {
		return nil, err
	}
	platforms := map[int64]string{}
	for _, sub := range subs {
		platforms[sub.ID] = sub.Platform
	}
	stats, err := a.Repo.TaskGroupStats(settings.Debug.TestItemLimit)
	if err != nil {
		return nil, err
	}
	groups := make([]TaskGroup, 0, len(stats))
	for _, stat := range stats {
		group := TaskGroup{SubscriptionID: stat.SubscriptionID, Status: "completed", Counts: stat.Counts, Total: stat.Total, ScanStatus: stat.ScanStatus, UpdatedAt: stat.UpdatedAt, Error: stat.Error, TestItemLimit: settings.Debug.TestItemLimit}
		// 状态汇总包含扫描任务；下载进度只统计媒体任务。
		for _, state := range []struct {
			name  string
			count int
		}{{"running", stat.Running}, {"queued", stat.Queued}, {"failed", stat.Failed}, {"cancelled", stat.Cancelled}} {
			if state.count > 0 {
				group.Status = state.name
				break
			}
		}
		if group.Status == "completed" && group.Counts["skipped"] == group.Total && group.Total > 0 {
			group.Status = "skipped"
		}
		// 休眠是调度展示状态，底层队列不改成取消或失败，取消按钮继续可用。
		if until := settings.Sleep.Until(platforms[group.SubscriptionID], time.Now()); !until.IsZero() && group.Counts["running"] == 0 && (stat.Queued > 0 || stat.Running > 0) {
			group.SleepUntil = &until
		}
		state, statusErr := a.Credentials.Status(platforms[group.SubscriptionID])
		if statusErr != nil {
			return nil, statusErr
		}
		group.PlatformDisabled = !state.PlatformEnabled
		if group.PlatformDisabled {
			group.BlockedReason = state.Reason
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].UpdatedAt.After(groups[j].UpdatedAt) })
	return groups, nil
}

// GroupAction 操作订阅内的当前批次；取消还会停止测试范围外的积压任务。
// 取消意图先原子提交，再停止引擎；调度与完成回写不能穿插在批次之间。
func (a *App) GroupAction(subscriptionID int64, action string) (count int, err error) {
	started := time.Now()
	a.mu.Lock()
	lockWait := time.Since(started)
	// 将锁等待与事务耗时分开，后续卡顿能从容器日志确认发生在哪一段。
	defer func() {
		a.mu.Unlock()
		slog.Info("任务控制完成", "operation", action, "subscription_id", subscriptionID,
			"count", count, "lock_wait", lockWait, "elapsed", time.Since(started), "error", err)
	}()
	if action == "cancel" {
		ids, err := a.Repo.CancelSubscriptionTasks(subscriptionID)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			if cancel := a.active[id]; cancel != nil {
				cancel()
			}
		}
		return len(ids), nil
	}
	settings, err := a.Repo.Settings()
	if err != nil {
		return 0, err
	}
	rows, err := a.Repo.TaskContentRows(subscriptionID, settings.Debug.TestItemLimit)
	if err != nil {
		return 0, err
	}
	// 重试恢复整个批次后再允许调度，仍只处理当前调试范围。
	count = 0
	for _, row := range rows {
		t := row.Task
		if t.SubscriptionID != subscriptionID {
			continue
		}
		if action == "retry" && (t.Status == "failed" || t.Status == "cancelled" || t.Status == "skipped") {
			err = a.retryTaskLocked(t.ID)
		} else {
			continue
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// GroupLog 按界面配置读取最近日志行，不遍历全部媒体任务或返回全部历史。
func (a *App) GroupLog(subscriptionID int64) (string, error) {
	sub, err := a.Repo.Subscription(subscriptionID)
	if err != nil {
		return "", err
	}
	settings, err := a.Repo.Settings()
	if err != nil {
		return "", err
	}
	limit := settings.UI.TaskLogLines
	return a.readSubscriptionLogTail(sub, limit)
}
