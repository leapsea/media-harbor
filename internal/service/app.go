package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"log/slog"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// App 持有接口依赖；入口负责注入 SQLite 和 CLI 适配器。
type App struct {
	Config      config.Config
	Repo        domain.Repository
	Engine      domain.Engine
	mu          sync.Mutex
	logMu       sync.Mutex
	avatarMu    sync.Mutex // 头像更新与移动归档共用锁，避免并发产生两个副本。
	loggers     map[string]*subscriptionLogWriter
	active      map[int64]context.CancelFunc
	wake        chan struct{}
	directories []DirectoryStatus
	wg          sync.WaitGroup
}

// New 不启动隐式后台协程，主程序明确控制生命周期。
// Repository 与 Engine 的实现仅在主程序中选择。
// 构造时的目录快照会在 Dashboard 展示。
func New(c config.Config, r domain.Repository, e domain.Engine) *App {
	return &App{Config: c, Repo: r, Engine: e, active: map[int64]context.CancelFunc{}, loggers: map[string]*subscriptionLogWriter{}, wake: make(chan struct{}, 1), directories: DetectDirectories(c)}
}

// NextRun 支持 @every 6h、@daily 和标准五字段 Cron。
// 五字段表达式不包含秒；固定周期统一使用 @every。
// 空周期表示用户仅手动执行任务。
func NextRun(schedule string, now time.Time) (time.Time, error) {
	if schedule == "" {
		return time.Time{}, nil
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	s, e := parser.Parse(schedule)
	if e != nil {
		return time.Time{}, e
	}
	return s.Next(now), nil
}

// SaveSubscription 只允许可编辑字段变化，固定目录名称在创建后保留。
// UI 的可编辑配置不覆盖扫描维护的身份和历史时间。
// 原有归档配置在卷失效后仍保留，允许用户修改其他选项。
func (a *App) SaveSubscription(s *domain.Subscription) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	u, e := url.Parse(strings.TrimSpace(s.URL))
	if e != nil || u.Host == "" {
		return fmt.Errorf("请输入有效任务 URL")
	}
	s.URL = u.String()
	created := s.ID == 0
	s.Mode = domain.TaskMode(s.Platform, s.URL)
	if s.Mode == "content" {
		s.Schedule = ""
		s.Enabled = false
	}
	if s.Platform != "youtube" && s.Platform != "instagram" {
		return fmt.Errorf("平台应为 youtube 或 instagram")
	}
	next, e := NextRun(s.Schedule, time.Now())
	if e != nil {
		return fmt.Errorf("定时表达式无效: %w", e)
	}
	if s.ID == 0 && s.Archive && !ArchiveAvailable(a.directories) {
		return fmt.Errorf("%s", directoryError(a.directories))
	}
	if s.ID != 0 {
		old, e := a.Repo.Subscription(s.ID)
		if e != nil {
			return e
		}
		if s.Platform != old.Platform {
			return fmt.Errorf("已有任务不能更换平台")
		}
		if s.Archive && !old.Archive && !ArchiveAvailable(a.directories) {
			return fmt.Errorf("%s", directoryError(a.directories))
		}
		s.StableID = old.StableID
		s.DisplayName = old.DisplayName
		s.CurrentName = old.CurrentName
		s.CreatedAt = old.CreatedAt
		s.LastScan = old.LastScan
		s.LastSuccess = old.LastSuccess
	} else {
		s.CreatedAt = time.Now()
		if s.DisplayName == "" {
			s.DisplayName = strings.Trim(u.Path, "/")
			if s.DisplayName == "" {
				s.DisplayName = "subscription-" + strconv.FormatInt(s.CreatedAt.UnixNano(), 10)
			}
		}
		s.CurrentName = s.DisplayName
	}
	s.NextRun = next
	if s.LastScan.IsZero() || s.Mode == "content" {
		s.NextRun = time.Time{}
	}
	if err := a.Repo.SaveSubscription(s); err != nil {
		return err
	}
	// 新建详情立即执行一次，主页仅保存配置，等待显式启动。
	if created && s.Mode == "content" {
		_, err := a.Repo.Enqueue(s.ID, 0, "resolve")
		if err == nil {
			a.wakeScheduler()
		}
		return err
	}
	return nil
}

// QueueScan 全量和增量共用平台枚举，唯一内容 ID 决定新增下载。
// 手动扫描不要求定时开关启用。
// 同一订阅只能有一个未结束的扫描记录。
func (a *App) QueueScan(id int64, kind string) (domain.Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, e := a.Repo.Subscription(id); e != nil {
		return domain.Task{}, e
	}
	if kind != "full" && kind != "scan" && kind != "catchup" {
		return domain.Task{}, fmt.Errorf("扫描类型无效")
	}
	tasks, e := a.Repo.Tasks()
	if e != nil {
		return domain.Task{}, e
	}
	for _, t := range tasks {
		if t.SubscriptionID == id && t.ContentID == 0 && (t.Status == "queued" || t.Status == "running") {
			return domain.Task{}, fmt.Errorf("该任务已有待执行或运行的扫描")
		}
	}
	task, err := a.Repo.Enqueue(id, 0, kind)
	if err == nil {
		a.wakeScheduler()
	}
	return task, err
}

// DeleteSubscription 取消相关任务后删除记录，保留实际文件。
// 中断信号先传给引擎，随后在事务内删除领域记录。
// 与该订阅关联的磁盘文件不参与记录删除。
func (a *App) DeleteSubscription(id int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	tasks, e := a.Repo.Tasks()
	if e != nil {
		return e
	}
	for _, t := range tasks {
		if t.SubscriptionID == id {
			if cancel := a.active[t.ID]; cancel != nil {
				cancel()
			}
		}
	}
	return a.Repo.DeleteSubscription(id)
}

// CancelTask 的状态修改与调度认领共用互斥锁。
// 数据库记录取消意图，后台结束时不得覆盖该状态。
// 内容状态同时变为取消，便于历史页面呈现。
func (a *App) CancelTask(id int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, e := a.Repo.Task(id)
	if e != nil {
		return e
	}
	if t.Status != "running" && t.Status != "queued" {
		return fmt.Errorf("任务已经结束")
	}
	if cancel := a.active[id]; cancel != nil {
		cancel()
	}
	t.Status = "cancelled"
	t.FinishedAt = time.Now()
	if t.ContentID != 0 {
		c, e := a.Repo.Content(t.ContentID)
		if e != nil {
			return e
		}
		c.Status = "cancelled"
		if e = a.Repo.SaveContent(&c); e != nil {
			return e
		}
	}
	return a.Repo.SaveTask(&t)
}

// RetryTask 复用失败任务及工作目录，不自动无限重试。
// 重试复用原任务 ID，因此能够复用下载中间文件。
// 新的尝试次数在调度器真正认领时增加。
func (a *App) RetryTask(id int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.retryTaskLocked(id)
}

// retryTaskLocked 供批量重试在同一锁内恢复，避免重新失败与解除策略交错。
func (a *App) retryTaskLocked(id int64) error {
	t, e := a.Repo.Task(id)
	if e != nil {
		return e
	}
	if t.Status != "failed" && t.Status != "cancelled" && t.Status != "skipped" {
		return fmt.Errorf("只能重试失败、取消或跳过的任务")
	}
	if a.active[id] != nil {
		return fmt.Errorf("任务正在停止，请稍后重试")
	}
	// 只有显式重试付费条目才解除频道策略，普通视频重试不改变限制。
	if t.ContentID != 0 {
		content, err := a.Repo.Content(t.ContentID)
		if err != nil {
			return err
		}
		sub, err := a.Repo.Subscription(t.SubscriptionID)
		if err != nil {
			return err
		}
		if sub.Platform == "youtube" && domain.IsPaidAvailability(content.Availability) {
			if err = a.Repo.AllowPaidVideos(paidChannel(content, sub)); err != nil {
				return err
			}
		}
	}
	t.Status = "queued"
	t.Error = ""
	t.FinishedAt = time.Time{}
	err := a.Repo.SaveTask(&t)
	if err == nil {
		a.wakeScheduler()
	}
	return err
}

// Settings 返回当前挂载条件下的新任务设置，不改变已创建任务的归档配置。
func (a *App) Settings() (domain.Settings, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	settings, err := a.Repo.Settings()
	if !ArchiveMounted(a.directories) {
		settings.AutoArchive = false
	}
	return settings, err
}

// 保存设置后，新的并发上限在下一次调度生效。
// 并发变小不主动取消已运行任务。
// 持久设置作为后续任务的执行快照。
func (a *App) SaveSettings(s domain.Settings) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.YouTubeConcurrent < 1 || s.InstagramConcurrent < 1 {
		return fmt.Errorf("并发数量至少为 1")
	}
	if s.Template == "" {
		return fmt.Errorf("目录模板不能为空")
	}
	// 语言是界面设置的一部分，只接受产品提供的五种选项。
	switch s.UI.Language {
	case "zh-CN", "en", "hi", "ru", "ja":
	default:
		return fmt.Errorf("不支持的界面语言")
	}
	if s.UI.RecentDownloads < 1 || s.UI.HistoryPageSize < 1 || s.UI.TaskPageSize < 1 {
		return fmt.Errorf("界面展示数量至少为 1")
	}
	// 0 关闭调试限制，正整数由界面自定义并用于扫描、调度与重试。
	if s.Debug.TestItemLimit < 0 {
		return fmt.Errorf("测试条目限制不能为负数")
	}
	// 未挂载时默认归档始终关闭，保存其他分类也遵循同一规则。
	if !ArchiveMounted(a.directories) {
		s.AutoArchive = false
	}
	err := a.Repo.SaveSettings(s)
	if err == nil {
		a.wakeScheduler()
	}
	return err
}

// Directories 返回快照副本，调用者不能修改应用状态。
func (a *App) Directories() []DirectoryStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]DirectoryStatus{}, a.directories...)
}

// Redetect 显式执行读写探测，不由前端轮询触发。
func (a *App) Redetect() []DirectoryStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.directories = DetectDirectories(a.Config)
	return append([]DirectoryStatus{}, a.directories...)
}

// Run 以单调度器处理定时和并发；入队与完成事件立即补充空闲下载名额。
// 每次检查只认领当前可运行任务，其余保留在 SQLite 队列。
// 退出时等待下载进程结束后再释放数据库。
func (a *App) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	a.runScheduler(ctx, ticker.C)
}

// wakeScheduler 合并重复通知，不阻塞生产者；调度器每次读取完整队列。
func (a *App) wakeScheduler() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// runScheduler 在启动时恢复队列；定时检查处理周期任务，事件处理下载补位。
func (a *App) runScheduler(ctx context.Context, ticks <-chan time.Time) {
	a.wakeScheduler()
	for {
		select {
		case <-ctx.Done():
			a.mu.Lock()
			for _, cancel := range a.active {
				cancel()
			}
			a.mu.Unlock()
			a.wg.Wait()
			a.closeLogs()
			return
		case <-ticks:
			a.flushLogs()
			a.wakeScheduler()
		case <-a.wake:
			if ctx.Err() != nil {
				continue
			}
			if e := a.tick(ctx); e != nil {
				slog.Error("调度失败", "error", e)
			}
		}
	}
}

// tick 在锁内认领任务，避免手动操作与调度互相覆盖状态。
// 调度器不在平台请求期间持锁。
// 扫描生产者与下载消费者分别受平台限额约束，互不占用执行名额。
func (a *App) tick(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	settings, e := a.Repo.Settings()
	if e != nil {
		return e
	}
	subs, e := a.Repo.Subscriptions()
	if e != nil {
		return e
	}
	tasks, e := a.Repo.Tasks()
	if e != nil {
		return e
	}
	subMap := map[int64]domain.Subscription{}
	busyScans := map[int64]bool{}
	scanning := map[int64]bool{}
	counts := map[string]int{}
	// 订阅快照同时用于平台计数和归档开关判断。
	for _, s := range subs {
		subMap[s.ID] = s
	}
	for _, t := range tasks {
		if (t.Status == "queued" || t.Status == "running") && t.ContentID == 0 {
			scanning[t.SubscriptionID] = true
		}
		if a.active[t.ID] != nil {
			if t.ContentID == 0 {
				busyScans[t.SubscriptionID] = true
			}
			counts[subMap[t.SubscriptionID].Platform+taskLane(t)]++
		}
	}
	// 手动暂停订阅只停止后续定时扫描，现有下载队列继续处理。
	for _, s := range subs {
		if domain.TaskMode(s.Platform, s.URL) != "content" && !s.LastScan.IsZero() && s.Enabled && s.Schedule != "" && !s.NextRun.After(time.Now()) && !scanning[s.ID] {
			if _, e = a.Repo.Enqueue(s.ID, 0, "scan"); e != nil {
				return e
			}
			s.NextRun, e = NextRun(s.Schedule, time.Now())
			if e != nil {
				return e
			}
			if e = a.Repo.SaveSubscription(&s); e != nil {
				return e
			}
		}
	}
	// 测试模式只认领每个订阅前 N 个下载，旧队列其余记录继续保留。
	allowedDownloads := map[int64]bool{}
	if settings.Debug.TestItemLimit > 0 {
		for _, s := range subs {
			for _, t := range selectedTasks(tasks, s.ID, settings.Debug.TestItemLimit) {
				if t.ContentID != 0 {
					allowedDownloads[t.ID] = true
				}
			}
		}
	}
	// 队列按创建顺序执行，扫描及下载分别计入所属平台并发。
	for i := len(tasks) - 1; i >= 0; i-- {
		t := tasks[i]
		s, exists := subMap[t.SubscriptionID]
		if !exists || t.Status != "queued" || (t.ContentID == 0 && busyScans[s.ID]) {
			continue
		}
		// 范围外测试条目保持原样；策略跳过不占用并发名额，也无需工作目录。
		if t.Kind == "download" {
			if settings.Debug.TestItemLimit > 0 && !allowedDownloads[t.ID] {
				continue
			}
			skipped, err := a.skipBlockedPaidLocked(&t, s)
			if err != nil {
				return err
			}
			if skipped {
				continue
			}
		}
		limit := settings.YouTubeConcurrent
		if s.Platform == "instagram" {
			limit = settings.InstagramConcurrent
		}
		if counts[s.Platform+taskLane(t)] >= limit {
			continue
		}
		if t.Kind == "download" && (!a.directories[1].Writable || (settings.Debug.TestItemLimit > 0 && !allowedDownloads[t.ID])) {
			continue
		}
		// 认领写入持久状态后，才创建后台执行上下文。
		taskCtx, cancel := context.WithCancel(ctx)
		t.Status = "running"
		t.StartedAt = time.Now()
		t.FinishedAt = time.Time{}
		t.Error = ""
		t.Attempts++
		t.Progress = nil
		if e = a.Repo.SaveTask(&t); e != nil {
			cancel()
			return e
		}
		a.active[t.ID] = cancel
		if t.ContentID == 0 {
			busyScans[s.ID] = true
		}
		counts[s.Platform+taskLane(t)]++
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.execute(taskCtx, t, s, settings) }()
	}
	// 已完成但尚未归档的记录在卷恢复后继续处理。
	if ArchiveAvailable(a.directories) {
		items, e := a.Repo.Contents()
		if e != nil {
			return e
		}
		for _, c := range items {
			s, ok := subMap[c.SubscriptionID]
			if ok && s.Archive && c.Status == "completed" && !c.Archived && c.Error == "" {
				if e = a.archive(&c); e != nil {
					c.Error = "归档失败: " + e.Error()
					if e = a.Repo.SaveContent(&c); e != nil {
						return e
					}
				}
			}
		}
	}
	return nil
}

// execute 的耗时引擎调用在锁外运行，最终领域状态在锁内提交。
// 引擎输出写入订阅日期日志，JSON 扫描结果直接转换为内容。
// 任务被删除或取消后，后台结果不会再次提交为完成。
func (a *App) execute(ctx context.Context, t domain.Task, s domain.Subscription, settings domain.Settings) {
	s.RequestLanguage = settings.UI.Language
	logStarted := time.Now()
	writer, e := a.beginTaskLog(s, logStarted)
	if e != nil {
		a.finish(t, e)
		return
	}
	defer a.endTaskLog(s, logStarted, writer)
	log := func(msg string) {
		if a.recordProgress(t.ID, msg) {
			return
		}
		a.writeTaskLog(writer, t, msg)
	}
	started := time.Now()
	if t.ContentID == 0 {
		log("开始解析订阅内容")
	} else {
		log("开始处理媒体任务")
	}
	if t.ContentID == 0 {
		log("解析订阅地址：" + s.URL)
	}
	defer func() {
		log(fmt.Sprintf("执行结束；耗时 %s", time.Since(started).Round(time.Millisecond)))
	}()
	if t.ContentID == 0 {
		if t.Kind == "catchup" {
			log("补全下载：复用已有媒体文件，补齐缺项并刷新元数据与所选评论")
		}
		// 等待期间定期报告耗时，不输出 Cookie 或引擎 JSON 原文。
		progressDone, progressStopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(progressStopped)
			ticker := time.NewTicker(config.ScanLogInterval)
			defer ticker.Stop()
			for {
				select {
				case <-progressDone:
					return
				case <-ticker.C:
					log(fmt.Sprintf("正在枚举媒体，已等待 %s", time.Since(started).Round(time.Second)))
				}
			}
		}()
		added, retried, completed, pending, skipped, discovered := 0, 0, 0, 0, 0, 0
		consume := func(batch domain.ScanResult) error {
			a.mu.Lock()
			defer a.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			current, err := a.Repo.Task(t.ID)
			if err != nil {
				return err
			}
			if current.Status != "running" {
				return context.Canceled
			}
			latest, err := a.Repo.Subscription(s.ID)
			if err != nil {
				return err
			}
			if batch.StableID != "" {
				latest.StableID = batch.StableID
			}
			if batch.CurrentName != "" {
				latest.CurrentName = batch.CurrentName
			}
			if err = a.Repo.SaveSubscription(&latest); err != nil {
				return err
			}
			for _, item := range batch.Items {
				if settings.Debug.TestItemLimit > 0 && discovered >= settings.Debug.TestItemLimit {
					break
				}
				x, y, z, w, v, err := a.reconcileScanLocked(s.ID, []domain.Content{item}, t.Kind == "catchup")
				if err != nil {
					return err
				}
				if x+y > 0 {
					a.wakeScheduler()
				}
				added += x
				retried += y
				completed += z
				pending += w
				skipped += v
				discovered++
				log(fmt.Sprintf("解析帖子 %s · %s；资源序号 %d；发布日期 %s；累计资源 %d 项；新增 %d 项；已存在 %d 项", item.RemoteID, item.Title, item.Asset, publicationLabel(item.Published), discovered, added, skipped+completed+pending))
			}
			return nil
		}
		result, scanErr := a.streamScan(ctx, s, settings.Debug.TestItemLimit, t.Kind == "catchup", log, consume)
		close(progressDone)
		<-progressStopped
		log(fmt.Sprintf("账号识别：稳定 ID=%s；当前名称=%s", result.StableID, result.CurrentName))
		// 新增与重复分别计数，重扫十条已有内容也能解释为何没有新下载。
		a.mu.Lock()
		// 重新读取订阅，保留扫描期间用户保存的配置。
		latest, readErr := a.Repo.Subscription(s.ID)
		current, taskErr := a.Repo.Task(t.ID)
		if readErr == nil && taskErr == nil && current.Status == "running" && ctx.Err() == nil {
			// 失败扫描也记录执行时间，最近成功时间只在无错误时推进。
			latest.LastScan = time.Now()
			if latest.Enabled && latest.Schedule != "" && domain.TaskMode(latest.Platform, latest.URL) != "content" {
				latest.NextRun, _ = NextRun(latest.Schedule, time.Now())
			}
			if result.StableID != "" {
				latest.StableID = result.StableID
			}
			if result.CurrentName != "" {
				latest.CurrentName = result.CurrentName
			}
			if scanErr == nil {
				latest.LastSuccess = time.Now()
			}
			// 作者元数据与任务目标分别保存，详情任务不会占用作者主页身份。
			e = a.Repo.SaveSubscription(&latest)

			if e != nil {
				scanErr = e
			}
		}
		a.mu.Unlock()
		if t.Kind == "catchup" {
			log(fmt.Sprintf("补全下载发现 %d 项；新增下载 %d 项；重新排队 %d 项；已完成跳过 %d 项；处理中跳过 %d 项", discovered, added, retried, completed, pending))
		} else {
			log(fmt.Sprintf("扫描发现 %d 项；新增下载 %d 项；已存在跳过 %d 项", discovered, added, skipped))
		}
		// 测试扫描的十条结果不能在日志中声称完整获取全部历史。
		if settings.Debug.TestItemLimit > 0 {
			log("本次为测试批次，不代表完整历史扫描")
		} else {
			log("扫描范围：平台当前可获取内容")
		}
		if scanErr != nil {
			log("扫描失败：" + scanErr.Error())
		} else {
			log("扫描完成；已发现媒体在下载队列中持续处理")
		}
		if ctx.Err() != nil {
			scanErr = ctx.Err()
		}
		a.finish(t, scanErr)
		return
	}
	a.mu.Lock()
	c, e := a.Repo.Content(t.ContentID)
	currentTask, taskReadErr := a.Repo.Task(t.ID)
	if e == nil && taskReadErr == nil && currentTask.Status == "running" {
		c.Status = "running"
		c.Error = ""
		e = a.Repo.SaveContent(&c)
		if e == nil {
			// 新一轮下载重新进入历史，已运行任务后续更新不会撤销清空。
			e = a.Repo.ShowDownloadHistory(c.ID)
		}
	}
	a.mu.Unlock()
	if e != nil {
		a.finish(t, e)
		return
	}
	// 订阅选项为整体覆盖，空值沿用全局默认。
	options := settings.YouTube
	if s.YouTube != nil {
		options = *s.YouTube
	}
	dir := a.WorkPath(t)
	log(fmt.Sprintf("开始下载帖子：%s · %s；资源序号 %d；发布日期 %s", c.RemoteID, c.Title, c.Asset, publicationLabel(c.Published)))
	log("请求资源地址：" + c.URL)
	e = a.Engine.Download(ctx, s, c, options, dir, log)
	// 只有媒体下载成功才获取头像；单纯扫描与空队列不会留下头像文件。
	if e == nil && ctx.Err() == nil {
		// 头像输出仅写日志，不更新媒体进度，归档持锁等待时不会反向申请任务锁。
		a.updateDownloadAvatar(ctx, s, func(msg string) { a.writeTaskLog(writer, t, msg) })
	}
	downloadErr := e
	// 以当前语言的下载元数据刷新标题，已存在的媒体文件也无需重新下载。
	if s.Platform == "youtube" {
		// 单视频元数据优先于扁平扫描；下载失败也保留已解析的真实日期。
		if published := engine.PublicationDate(dir); !published.IsZero() {
			c.Published = published
		}
		if title := engine.PublicationTitle(dir); title != "" {
			c.Title = title
		}
		channel, availability := engine.PublicationAccess(dir)
		if channel != "" {
			c.ChannelID = channel
		}
		if availability != "" {
			c.Availability = availability
		}
	}
	a.mu.Lock()
	current, readErr := a.Repo.Task(t.ID)
	if readErr == nil && current.Status == "running" && ctx.Err() == nil {
		var skipped *domain.PaidVideoSkippedError
		policySkip := errors.As(downloadErr, &skipped)
		// 只有实际引擎失败才阻断后续付费视频，本地归位失败不改变策略。
		if s.Platform == "youtube" && downloadErr != nil && !policySkip && domain.IsPaidAvailability(c.Availability) {
			if channel := paidChannel(c, s); channel != "" {
				if blockErr := a.Repo.BlockPaidVideos(channel, t.ID); blockErr != nil {
					log("保存付费跳过策略失败：" + blockErr.Error())
				} else {
					log("付费视频下载失败：该 UP 主后续付费视频将直接跳过")
				}
			}
		}
		// 即使后续阶段失败，也记录已经完成的产物，重试只补剩余部分。
		files, fileErr := taskFiles(dir)
		if fileErr == nil && len(files) > 0 {
			if placeErr := a.placeFiles(s, &c, dir); placeErr != nil && e == nil {
				e = placeErr
			}
		} else if e == nil && len(engine.Inventory(c, "").Paths) == 0 {
			e = fileErr
		}
		if e == nil {
			c.Status = "completed"
			c.Error = ""
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				log("临时目录清理失败：" + cleanupErr.Error())
			}
			log("下载完成；保存文件：" + strings.Join(c.Paths, "，"))
		} else {
			c.Status = "failed"
			c.Error = e.Error()
			if policySkip {
				c.Status = "skipped"
				// 丢弃本次解析缓存，只移除空工作目录，已有媒体和分片继续保留供重试。
				_ = os.Remove(filepath.Join(dir, ".metadata.json"))
				_ = os.Remove(dir)
			}
		}
		if saveErr := a.Repo.SaveContent(&c); saveErr != nil {
			e = saveErr
		}
	}
	a.mu.Unlock()
	if e != nil {
		log(e.Error())
	}
	if ctx.Err() != nil {
		e = ctx.Err()
	}
	a.finish(t, e)
}

// publicationLabel 不将尚未解析的日期零值显示成公元 1 年。
func publicationLabel(date time.Time) string {
	if date.IsZero() {
		return "待解析"
	}
	return date.Format("2006-01-02")
}

// finish 不覆盖用户取消状态；服务停止的任务回到队列。
// Context 取消且数据库仍为 running，表示服务关闭。
// 用户取消已提前落库，使用 cancelled 状态保留其意图。
func (a *App) finish(t domain.Task, taskErr error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		delete(a.active, t.ID)
		a.wakeScheduler()
	}()
	current, e := a.Repo.Task(t.ID)
	if e != nil || current.Status != "running" {
		return
	}
	current.FinishedAt = time.Now()
	current.Status = "completed"
	current.Error = ""
	if taskErr != nil {
		current.Status = "failed"
		current.Error = taskErr.Error()
		var skipped *domain.PaidVideoSkippedError
		if errors.As(taskErr, &skipped) {
			current.Status = "skipped"
			current.Progress = []domain.TransferProgress{{Kind: "video", Status: "skipped"}}
		}
	}
	if errors.Is(taskErr, context.Canceled) {
		current.Status = "queued"
		current.Error = "服务停止，等待继续执行"
		current.FinishedAt = time.Time{}
		if current.ContentID != 0 {
			c, readErr := a.Repo.Content(current.ContentID)
			if readErr == nil {
				c.Status = "queued"
				_ = a.Repo.SaveContent(&c)
			}
		}
	}
	if cancel := a.active[t.ID]; cancel != nil {
		cancel()
	}
	if e = a.Repo.SaveTask(&current); e != nil {
		slog.Error("保存任务失败", "error", e)
	}
}

// RetryArchive 只重新搬运已有文件，无需重新下载。
// 归档重试独立于下载任务，避免再次访问远程平台。
// 已移动的文件位置逐个写回数据库。
func (a *App) RetryArchive(id int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !ArchiveAvailable(a.directories) {
		return fmt.Errorf("%s", directoryError(a.directories))
	}
	c, e := a.Repo.Content(id)
	if e != nil {
		return e
	}
	if c.Status != "completed" {
		return fmt.Errorf("内容未下载完成")
	}
	c.Error = ""
	if e = a.archive(&c); e != nil {
		c.Error = "归档失败: " + e.Error()
		_ = a.Repo.SaveContent(&c)
	}
	return e
}

// taskLane 区分扫描与下载的独立平台计数，媒体下载可共享同一配置下的并发额度。
func taskLane(t domain.Task) string {
	if t.ContentID == 0 {
		return ":scan"
	}
	return ":download"
}
