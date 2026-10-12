package service

import (
	"fmt"
	"media-harbor/internal/domain"
	"sort"
	"strings"
	"time"
)

// taskLabels 用于订阅日志，界面无需将内部状态代码解释给用户。
var taskLabels = map[string]string{"resolve": "内容解析", "catchup": "补全下载", "full": "全量扫描", "scan": "增量扫描", "download": "下载", "queued": "待执行", "running": "运行中", "completed": "已完成", "failed": "失败", "cancelled": "已取消"}

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
	tasks, err := a.Repo.Tasks()
	if err != nil {
		return nil, err
	}
	groups := []TaskGroup{}
	// 一次分组，避免每个订阅重复复制和排序整个队列；保留首次出现顺序。
	bySubscription := map[int64][]domain.Task{}
	order := []int64{}
	for _, task := range tasks {
		if _, exists := bySubscription[task.SubscriptionID]; !exists {
			order = append(order, task.SubscriptionID)
		}
		bySubscription[task.SubscriptionID] = append(bySubscription[task.SubscriptionID], task)
	}
	for _, subscriptionID := range order {
		group := TaskGroup{SubscriptionID: subscriptionID, Status: "completed", Counts: map[string]int{"queued": 0, "running": 0, "completed": 0, "failed": 0, "cancelled": 0}, TestItemLimit: settings.Debug.TestItemLimit}
		selected := selectedTasks(bySubscription[subscriptionID], subscriptionID, settings.Debug.TestItemLimit)
		// 扫描仅参与总体状态，下载计数中不混入扫描成功记录。
		states := map[string]int{}
		for _, t := range selected {
			states[t.Status]++
			if t.ContentID == 0 {
				group.ScanStatus = t.Status
			} else {
				group.Counts[t.Status]++
				group.Total++
			}
			// 最近活动覆盖创建、开始与结束，重试后仍能看到本次更新时间。
			changed := t.CreatedAt
			if t.StartedAt.After(changed) {
				changed = t.StartedAt
			}
			if t.FinishedAt.After(changed) {
				changed = t.FinishedAt
			}
			if changed.After(group.UpdatedAt) {
				group.UpdatedAt = changed
			}
			if t.Error != "" {
				group.Error = t.Error
			}
		}
		// 有运行或等待时优先展示进行中；否则显式呈现失败或取消。
		for _, state := range []string{"running", "queued", "failed", "cancelled"} {
			if states[state] > 0 {
				group.Status = state
				break
			}
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
	count := 0
	for _, t := range selected {
		if t.SubscriptionID != subscriptionID {
			continue
		}
		if action == "cancel" && (t.Status == "queued" || t.Status == "running") {
			err = a.CancelTask(t.ID)
		} else if action == "retry" && (t.Status == "failed" || t.Status == "cancelled") {
			err = a.RetryTask(t.ID)
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

// GroupLog 将当前扫描和下载日志汇总，显示任务标识和现有状态。
// 每个文件由任务执行器追加，聚合读取不会改写历史日志。
func (a *App) GroupLog(subscriptionID int64) (string, error) {
	settings, err := a.Repo.Settings()
	if err != nil {
		return "", err
	}
	sub, err := a.Repo.Subscription(subscriptionID)
	if err != nil {
		return "", err
	}
	tasks, err := a.Repo.Tasks()
	if err != nil {
		return "", err
	}
	selected := selectedTasks(tasks, subscriptionID, settings.Debug.TestItemLimit)
	var result strings.Builder
	fmt.Fprintf(&result, "任务：%s · 平台：%s\n地址：%s\n", sub.DisplayName, sub.Platform, sub.URL)
	if settings.Debug.TestItemLimit > 0 {
		fmt.Fprintf(&result, "测试模式：只处理前 %d 个媒体条目，其余历史记录保留\n", settings.Debug.TestItemLimit)
	}
	// 扫描放在前面，使用户打开日志时先看到发现结果。
	sort.SliceStable(selected, func(i, j int) bool {
		if (selected[i].ContentID == 0) != (selected[j].ContentID == 0) {
			return selected[i].ContentID == 0
		}
		return selected[i].ID < selected[j].ID
	})
	for _, t := range selected {
		fmt.Fprintf(&result, "\n—— 任务 #%d · %s · %s · 第 %d 次 ——\n", t.ID, taskLabels[t.Kind], taskLabels[t.Status], t.Attempts)
		fmt.Fprintf(&result, "创建时间：%s\n", t.CreatedAt.Local().Format("2006-01-02 15:04:05"))
		raw, e := a.taskLog(t)
		if e != nil {
			return "", e
		}
		result.WriteString(compactProgressLog(raw))
	}
	return result.String(), nil
}

// compactProgressLog 在展示历史日志时保留每秒最后一条下载进度。
// 原日志文件不改写，错误、最终汇总及非进度内容全部保留。
func compactProgressLog(raw string) string {
	var result strings.Builder
	pending, key := "", ""
	flush := func() {
		if pending != "" {
			result.WriteString(pending + "\n")
			pending = ""
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		progress := len(line) > 10 && strings.HasPrefix(line, "[") && strings.Contains(line, "[download]") && strings.Contains(line, "%") && strings.Contains(line, "ETA")
		if progress {
			stamp := line[:strings.IndexByte(line, ']')+1]
			if stamp != key {
				flush()
				key = stamp
			}
			pending = line
		} else {
			flush()
			key = ""
			result.WriteString(line + "\n")
		}
	}
	flush()
	return result.String()
}
