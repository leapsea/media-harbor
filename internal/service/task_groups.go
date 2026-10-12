package service

import (
	"fmt"
	"media-harbor/internal/domain"
	"sort"
	"strings"
	"time"
	"unicode"
)

// taskLabels 用于订阅日志，界面无需将内部状态代码解释给用户。
var taskLabels = map[string]string{"resolve": "内容解析", "catchup": "补全下载", "full": "全量扫描", "scan": "增量扫描", "download": "下载", "queued": "待执行", "running": "运行中", "completed": "已完成", "failed": "失败", "cancelled": "已取消", "skipped": "已跳过"}

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
		if t.ContentID != 0 {
			if content, err := a.Repo.Content(t.ContentID); err == nil {
				fmt.Fprintf(&result, "%s · %s\n", content.RemoteID, content.Title)
			}
		}
		raw, e := a.taskLog(t)
		if e != nil {
			return "", e
		}
		result.WriteString(conciseTaskLog(raw))
	}
	return result.String(), nil
}

// conciseTaskLog 只展示最近一次执行的业务阶段和异常，完整日志文件保持原样。
// 文件地址、播放器请求、分片百分比属于引擎诊断；字节进度由详情面板独立呈现。
func conciseTaskLog(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	start := 0
	for i, line := range lines {
		if strings.Contains(line, "开始 ") && strings.Contains(line, " 任务 #") {
			start = i
		}
	}
	var result strings.Builder
	warnings := map[string]bool{}
	latestDiscovery, latestWait := -1, -1
	finishedScan := false
	for i, line := range lines[start:] {
		if strings.Contains(line, "发现媒体 ") {
			latestDiscovery = i
		}
		if strings.Contains(line, "正在枚举媒体，") {
			latestWait = i
		}
		if strings.Contains(line, "扫描发现 ") {
			finishedScan = true
		}
	}
	for i, line := range lines[start:] {
		message := line
		stamp := ""
		if strings.HasPrefix(line, "[") {
			if end := strings.Index(line, "] "); end >= 0 {
				message = line[end+2:]
				stamp = line[:end+2]
			}
		}
		if strings.HasPrefix(message, "发现媒体 ") && (finishedScan || i != latestDiscovery) {
			continue
		}
		if strings.HasPrefix(message, "正在枚举媒体，") && i != latestWait {
			continue
		}
		lower := strings.ToLower(message)
		if strings.Contains(lower, "warning") {
			if warnings[message] {
				continue
			}
			warnings[message] = true
		}
		if strings.Contains(lower, "warning") || strings.Contains(lower, "error") || strings.Contains(message, "失败") {
			result.WriteString(line + "\n")
			continue
		}
		noise := false
		for _, prefix := range []string{"[", "frame=", "size=", "Deleting original file ", "任务：", "测试模式：", "平台标题语言：", "媒体地址：", "下载工作目录：", "读取下载元数据；", "主任务已开始处理；"} {
			if strings.HasPrefix(message, prefix) {
				noise = true
				break
			}
		}
		if noise || strings.TrimSpace(message) == "" {
			continue
		}
		if strings.HasPrefix(message, "下载完成；保存文件：") {
			files := strings.Split(strings.TrimPrefix(message, "下载完成；保存文件："), "，")
			line = stamp + fmt.Sprintf("下载完成；保存 %d 个文件", len(files))
		}
		if strings.HasPrefix(message, "主任务：复用已下载视频：") {
			line = stamp + "主任务：复用已下载视频"
		}
		if strings.ContainsFunc(message, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
			result.WriteString(line + "\n")
		}
	}
	return result.String()
}
