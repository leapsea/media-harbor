package service

import (
	"media-harbor/internal/domain"
	"sort"
	"time"
)

// TaskGroup 将一个订阅的扫描和下载状态收口为一行，底层任务仍独立持久化。
// 下载统计与扫描状态分开，避免扫描成功被误认为下载完成。
type TaskGroup struct {
	SubscriptionID int64          `json:"subscription_id"`
	Status         string         `json:"status"`
	ScanStatus     string         `json:"scan_status"`
	Counts         map[string]int `json:"counts"`
	Total          int            `json:"total"`
	Error          string         `json:"error"`
	UpdatedAt      time.Time      `json:"updated_at"`
	TestItemLimit  int            `json:"test_item_limit"`
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
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].UpdatedAt.After(groups[j].UpdatedAt) })
	return groups, nil
}

// GroupAction 操作订阅内的当前批次；取消还会停止测试范围外的积压任务。
// 复用已有取消和重试流程，保持运行 Context 与数据库状态一致。
func (a *App) GroupAction(subscriptionID int64, action string) (int, error) {
	settings, err := a.Repo.Settings()
	if err != nil {
		return 0, err
	}
	tasks, err := a.Repo.Tasks()
	if err != nil {
		return 0, err
	}
	selected := selectedTasks(tasks, subscriptionID, settings.Debug.TestItemLimit)
	// 取消代表停止整个订阅的现有队列，不让范围外积压随后恢复。
	if action == "cancel" {
		selected = tasks
	}
	// 重试恢复整个批次后再允许调度，避免快速失败与逐条解除策略相互覆盖。
	if action == "retry" {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	count := 0
	for _, t := range selected {
		if t.SubscriptionID != subscriptionID {
			continue
		}
		if action == "cancel" && (t.Status == "queued" || t.Status == "running") {
			err = a.CancelTask(t.ID)
		} else if action == "retry" && (t.Status == "failed" || t.Status == "cancelled" || t.Status == "skipped") {
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

// GroupLog 直接读取该订阅按日期划分的共享日志，不再遍历任务或逐个打开文件。
func (a *App) GroupLog(subscriptionID int64) (string, error) {
	sub, err := a.Repo.Subscription(subscriptionID)
	if err != nil {
		return "", err
	}
	return a.readSubscriptionLogs(sub)
}
