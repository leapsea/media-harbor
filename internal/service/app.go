package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/robfig/cron/v3"
	"log/slog"
	"media-harbor/internal/config"
	"media-harbor/internal/credentials"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"media-harbor/internal/logtext"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// App 持有接口依赖；入口负责注入 SQLite 和 CLI 适配器。
type App struct {
	Credentials      *credentials.Store
	credentialPaused map[int64]bool
	Config           config.Config
	Repo             domain.Repository
	Engine           domain.Engine
	mu               sync.Mutex
	logMu            sync.Mutex
	avatarMu         sync.Mutex // 头像更新与移动归档共用锁，避免并发产生两个副本。
	loggers          map[string]*subscriptionLogWriter
	active           map[int64]context.CancelFunc
	activeInfo       map[int64]runningTaskInfo
	queueWaits       map[int64]domain.PlatformRun
	sleepUntil       map[string]time.Time
	wake             chan struct{}
	directories      []DirectoryStatus
	wg               sync.WaitGroup
	schedulerPhase   atomic.Pointer[schedulerPhase]
}

// New 不启动隐式后台协程，主程序明确控制生命周期。
// Repository 与 Engine 的实现仅在主程序中选择。
// 构造时的目录快照会在 Dashboard 展示。
func New(c config.Config, r domain.Repository, e domain.Engine) *App {
	if configurable, ok := e.(domain.ConfigurableEngine); ok {
		if settings, err := r.Settings(); err == nil {
			configurable.Configure(settings)
		}
	}
	auth := credentials.New(c.DataDir)
	if owner, ok := e.(interface{ CredentialStore() *credentials.Store }); ok && owner.CredentialStore() != nil {
		auth = owner.CredentialStore()
	}
	auth.Activate()
	app := &App{Credentials: auth, credentialPaused: map[int64]bool{}, Config: c, Repo: r, Engine: e, active: map[int64]context.CancelFunc{}, activeInfo: map[int64]runningTaskInfo{}, queueWaits: map[int64]domain.PlatformRun{}, loggers: map[string]*subscriptionLogWriter{}, wake: make(chan struct{}, 1), directories: DetectDirectories(c)}
	// 启动时校正现有头像后缀，保持旧接口及作者共享身份。
	app.migrateAvatarExtensions()
	return app
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
	if s.YouTube != nil {
		if err := s.YouTube.ValidateComments(); err != nil {
			return err
		}
	}
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
		if s.CurrentName == "" {
			s.CurrentName = s.DisplayName
		}
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

// QueueScan 全量重建详情并覆盖文件，增量在已有索引处停止，补全保留记录检查缺项。
// 历史逻辑：全量和增量共用平台枚举，仅以唯一内容 ID 决定新增下载。
// 手动扫描不要求定时开关启用。
// 同一订阅只能有一个未结束的扫描记录。
func (a *App) QueueScan(id int64, kind string) (domain.Task, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sub, e := a.Repo.Subscription(id)
	if e != nil {
		return domain.Task{}, e
	}
	if kind != "full" && kind != "scan" && kind != "catchup" {
		return domain.Task{}, fmt.Errorf("扫描类型无效")
	}
	if kind == "full" {
		// 清理、取消意图与新扫描原子提交；成功后才停止旧引擎，不等待进程收尾。
		task, err := a.Repo.ReinitializeSubscription(id)
		if err != nil {
			return task, err
		}
		for taskID, info := range a.activeInfo {
			if info.SubscriptionID == id && a.active[taskID] != nil {
				a.active[taskID]()
			}
		}
		if err := a.writeSubscriptionLog(sub, task, time.Now(), "[全量初始化] 已清空帖子任务与下载资源记录；旧批次已取消；重新采集并覆盖本地文件"); err != nil {
			slog.Warn("写入初始化日志失败", "subscription_id", id, "error", err)
		}
		a.wakeScheduler()
		return task, nil
	}
	// 历史全量入口与增量一样仅 Enqueue，不清空详情，也不覆盖已有媒体。
	tasks, e := a.Repo.SubscriptionTasks(id)
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
	tasks, e := a.Repo.SubscriptionTasks(id)
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
	t.QueuedAt, t.SourceTaskID = time.Now(), 0
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
	if err := s.YouTube.ValidateComments(); err != nil {
		return err
	}
	s.YouTube = config.CommentDefaults(s.YouTube)
	if s.YouTubeConcurrent < 1 || s.InstagramConcurrent < 1 {
		return fmt.Errorf("并发数量至少为 1")
	}
	if s.Pipeline.QueueCapacity < 1 || s.Pipeline.InstagramSourceCacheTTLMinutes < 1 || s.Pipeline.InstagramSourceCacheCapacity < 1 {
		return fmt.Errorf("队列容量与缓存配置必须为正整数")
	}
	for _, producer := range []domain.ProducerSettings{s.Pipeline.Instagram, s.Pipeline.YouTube} {
		if producer.WindowSeconds < 1 || producer.MaxRequests < 1 || producer.MinIntervalSeconds < 0 || producer.CooldownSeconds < 0 {
			return fmt.Errorf("请求窗口与请求数必须为正整数，间隔与冷却不能为负数")
		}
	}
	// 客户端保存当前完整结构，不从已保存数据补入缺席字段。
	if err := s.Sleep.Validate(); err != nil {
		return err
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
	if s.UI.RecentDownloads < 1 || s.UI.HistoryPageSize < 1 || s.UI.TaskPageSize < 1 || s.UI.TaskLogLines < 1 {
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
		if configurable, ok := a.Engine.(domain.ConfigurableEngine); ok {
			configurable.Configure(s)
		}
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
	// 诊断使用原子快照，不等待调度器正在等待的应用锁或日志锁。
	diagnosticsDone := make(chan struct{})
	go func() { defer close(diagnosticsDone); a.watchScheduler(ctx) }()
	defer func() { <-diagnosticsDone }()
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
			a.markSchedulerPhase("刷新日志缓冲")
			a.flushLogs()
			a.schedulerPhase.Store(nil)
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
// 每个平台一次执行一个配置；当前配置的扫描与媒体下载仍使用独立名额。
func (a *App) tick(ctx context.Context) error {
	a.markSchedulerPhase("等待应用锁")
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.schedulerPhase.Store(nil)
	a.markSchedulerPhase("读取设置")
	settings, e := a.Repo.Settings()
	if e != nil {
		return e
	}
	a.markSchedulerPhase("读取任务配置")
	subs, e := a.Repo.Subscriptions()
	if e != nil {
		return e
	}
	a.markSchedulerPhase("读取持久任务队列")
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
			// 保留进程所属平台，删除记录或取消后也不能提前启动下一位博主。
			if _, ok := a.activeInfo[t.ID]; !ok {
				a.activeInfo[t.ID] = runningTaskInfo{Platform: subMap[t.SubscriptionID].Platform, SubscriptionID: t.SubscriptionID, Lane: taskLane(t)}
			}
		}
	}
	for id, info := range a.activeInfo {
		if a.active[id] != nil {
			counts[info.Platform+info.Lane]++
		}
	}
	// 无Instagram凭据或当前SDK仍携带已停用/被替换的Cookie时停止该进程。
	credentialStates := map[string]credentials.Status{}
	for _, platform := range []string{"instagram", "youtube"} {
		state, err := a.Credentials.Status(platform)
		if err != nil {
			return err
		}
		credentialStates[platform] = state
	}
	for id, info := range a.activeInfo {
		state := credentialStates[info.Platform]
		if cancel := a.active[id]; cancel != nil && (!state.PlatformEnabled || info.UsesCookie && (!state.Usable || state.Revision != info.CookieRevision)) {
			a.credentialPaused[id] = true
			cancel()
		}
	}
	// 同时到期的周期任务按到期时间进入队列，避免配置列表顺序决定先后。
	sort.SliceStable(subs, func(i, j int) bool {
		if subs[i].NextRun.Equal(subs[j].NextRun) {
			return subs[i].ID < subs[j].ID
		}
		return subs[i].NextRun.Before(subs[j].NextRun)
	})
	// 手动暂停订阅只停止后续定时扫描，现有下载队列继续处理。
	for _, s := range subs {
		if domain.TaskMode(s.Platform, s.URL) != "content" && !s.LastScan.IsZero() && s.Enabled && s.Schedule != "" && !s.NextRun.After(time.Now()) && !scanning[s.ID] {
			var queued domain.Task
			if queued, e = a.Repo.Enqueue(s.ID, 0, "scan"); e != nil {
				return e
			}
			tasks = append(tasks, queued)
			s.NextRun, e = NextRun(s.Schedule, time.Now())
			if e != nil {
				return e
			}
			if e = a.Repo.SaveSubscription(&s); e != nil {
				return e
			}
		}
	}
	// 到期任务仍入队；按平台计算休眠，当前媒体收尾不被取消。
	// 原全局逻辑在此直接 return nil，会阻止另一平台认领任务。
	if a.sleepUntil == nil {
		a.sleepUntil = map[string]time.Time{}
	}
	for _, platform := range []string{"instagram", "youtube"} {
		until := settings.Sleep.Until(platform, time.Now())
		if !until.Equal(a.sleepUntil[platform]) {
			if !until.IsZero() {
				slog.Info("平台进入休眠，暂停生产及新下载", "stage", "sleep", "platform", platform, "until", until, "timezone", settings.Sleep.For(platform).Timezone)
			} else if !a.sleepUntil[platform].IsZero() {
				slog.Info("平台休眠结束，恢复原队列", "stage", "sleep", "platform", platform)
			}
			a.sleepUntil[platform] = until
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
	// 平台执行权先于线程名额判断，空闲线程不能启动另一个博主。
	runs, e := a.platformQueueLocked(tasks, subMap, allowedDownloads, settings.Debug.TestItemLimit)
	if e != nil {
		return e
	}
	// 认领复用平台队列排好的快照，不再次排序完整历史。
	a.markSchedulerPhase("认领任务与分配并发名额")
	for _, t := range tasks {
		s, exists := subMap[t.SubscriptionID]
		// 只跳过休眠平台的新工作，另一平台照常执行其队列。
		if !a.sleepUntil[s.Platform].IsZero() {
			continue
		}
		if !exists || !credentialStates[s.Platform].PlatformEnabled || t.Status != "queued" || (t.ContentID == 0 && busyScans[s.ID]) {
			continue
		}
		if !belongsToPlatformRun(t, runs[s.Platform]) {
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
				a.wakeScheduler() // 当前批次全部跳过时，也立即交接给队列中的下一位博主。
				continue
			}
		}
		limit := settings.YouTubeConcurrent
		if s.Platform == "instagram" {
			limit = settings.InstagramConcurrent
		}
		// 平台生产者恒为一个，下载并发不会提高采集和解析并行度。
		if t.ContentID == 0 {
			limit = 1
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
		a.activeInfo[t.ID] = runningTaskInfo{Platform: s.Platform, SubscriptionID: s.ID, Lane: taskLane(t), UsesCookie: credentialStates[s.Platform].Usable, CookieRevision: credentialStates[s.Platform].Revision}
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
				a.markSchedulerPhase(fmt.Sprintf("归档内容 #%d（配置 #%d）", c.ID, c.SubscriptionID))
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
	latest, loadErr := a.Repo.Subscription(t.SubscriptionID)
	if loadErr != nil {
		a.finish(t, loadErr)
		return
	}
	currentSettings, loadErr := a.Repo.Settings()
	if loadErr != nil {
		a.finish(t, loadErr)
		return
	}
	s, settings = latest, currentSettings
	s.RequestLanguage = settings.UI.Language
	logStarted := time.Now()
	writer, e := a.beginTaskLog(s, logStarted)
	if e != nil {
		a.finish(t, e)
		return
	}
	defer a.endTaskLog(s, logStarted, writer)
	cookie, _ := a.Credentials.Status(s.Platform)
	presentation := newTaskLogPresentation(time.Now())
	log := func(msg string) {
		if reason := credentials.FailureReason(s.Platform, msg); cookie.Usable && reason != "" {
			if changed, err := a.Credentials.Disable(s.Platform, cookie.Revision, reason); changed && err == nil {
				slog.Warn("Cookie已停用", "stage", "authentication", "platform", s.Platform, "task_id", t.ID, "reason", reason)
				a.wakeScheduler()
			}
		}
		if a.recordProgress(t.ID, msg) {
			return
		}
		if message := presentation.message(msg, time.Now()); message != "" {
			a.writeTaskLog(writer, t, message)
		}
		// 历史日志直接写入所有SDK提示，同一元数据翻译警告会逐视频重复输出。
	}
	started := time.Now()
	if t.ContentID == 0 {
		log(fmt.Sprintf("开始解析订阅内容；扫描类型 %s；平台 %s；调试限制 %d", t.Kind, s.Platform, settings.Debug.TestItemLimit))
	} else {
		limit := settings.YouTubeConcurrent
		if s.Platform == "instagram" {
			limit = settings.InstagramConcurrent
		}
		log(fmt.Sprintf("开始处理媒体任务；平台 %s；配置 #%d；平台并发上限 %d", s.Platform, s.ID, limit))
	}
	if t.ContentID == 0 {
		log("解析订阅地址：" + s.URL)
	}
	defer func() {
		for _, message := range presentation.repeatedWarnings() {
			a.writeTaskLog(writer, t, message)
		}
		log("[执行结束] 耗时 " + logtext.Duration(time.Since(started)))
	}()
	if t.ContentID == 0 {
		if t.Kind == "catchup" {
			log("补全下载：复用已有媒体文件，补齐缺项并刷新元数据与所选评论")
		} else if t.Kind == "scan" {
			log("增量扫描：从最新内容开始，遇到本地已有媒体即结束；历史缺失项请使用补全下载")
		}
		// 等待期间定期报告耗时，不输出 Cookie 或引擎 JSON 原文。
		progressDone, progressStopped := make(chan struct{}), make(chan struct{})
		var queueWaiting atomic.Bool
		// 背压期间只输出消费者快照，不把暂停读取误报为正在解析平台。
		scanLog := func(message string) {
			if strings.HasPrefix(message, "下载队列已满") {
				queueWaiting.Store(true)
			}
			if strings.HasPrefix(message, "下载队列已释放容量") {
				queueWaiting.Store(false)
			}
			log(message)
		}
		go func() {
			defer close(progressStopped)
			ticker := time.NewTicker(config.ScanLogInterval)
			defer ticker.Stop()
			for {
				select {
				case <-progressDone:
					return
				case <-ticker.C:
					// 历史资产：仅以 !queueWaiting.Load() 判断是否输出平台等待日志。
					sleeping, _ := a.Repo.Settings()
					if !queueWaiting.Load() && sleeping.Sleep.Until(s.Platform, time.Now()).IsZero() {
						if message := presentation.idleMessage(time.Now(), started); message != "" {
							log(message)
						}
					}
					// 历史日志每5秒输出“等待平台返回媒体”，与请求间隔和请求响应重复。
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
				item.SourceTaskID = t.ID
				if settings.Debug.TestItemLimit > 0 && discovered >= settings.Debug.TestItemLimit {
					break
				}
				var x, y, z, w, v int
				var err error
				if t.Kind == "full" {
					x, y, v, err = a.reconcileFullScanLocked(s.ID, item)
				} else {
					x, y, z, w, v, err = a.reconcileScanLocked(s.ID, []domain.Content{item}, t.Kind == "catchup")
				}
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
				// 媒体身份放在首行，标题与地址独立展示并保留同一任务编号。
				presentation.progress(discovered, added, skipped+completed+pending)
				log(scanItemLog(s.Platform, item, x, y, z, w, v, discovered, added, skipped+completed+pending))
				// 历史日志将地址、标题和统计挤在一行，并为YouTube输出“资源序号0”。
			}
			return nil
		}
		result, scanErr := a.streamScan(ctx, s, settings.Debug.TestItemLimit, t.Kind, scanLog, consume)
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
		log(fmt.Sprintf("[扫描汇总] 发现 %d / 新增 %d / 重新排队 %d / 已存在 %d · 已完成 %d / 处理中 %d / 重复记录 %d · 耗时 %s", discovered, added, retried, completed+pending+skipped, completed, pending, skipped, logtext.Duration(time.Since(started))))
		// 历史汇总只将 skipped 计入“已存在”，未包含完成项和处理中条目。
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
	options := settings.YouTubeOptionsFor(s)
	if _, planErr := a.registerPost(ctx, s.ID, s.Platform, c, options); planErr != nil {
		a.finish(t, planErr)
		return
	}
	dir := a.WorkPath(t)
	log(mediaItemLog(s.Platform, c, "开始下载"))
	// 历史日志将视频称作帖子，并输出无意义的资源序号零与未知发布日期。
	download := c
	if t.Overwrite {
		// 文件索引用于原位置归位，传给下载器的副本不允许复用初始化前的产物。
		download.Paths, download.Files, download.Tags = nil, nil, nil
		log("[全量下载] 忽略原文件缓存；媒体 " + c.RemoteID + "；下载完成后覆盖原位置")
	}
	e = a.Engine.Download(ctx, s, download, options, dir, log)
	log(fmt.Sprintf("下载引擎已返回；成功 %t；取消 %t；准备读取产物并提交完成状态", e == nil, ctx.Err() != nil))
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
	log("等待完成阶段应用锁")
	completionWait := time.Now()
	a.mu.Lock()
	log(fmt.Sprintf("已取得完成阶段应用锁；等待 %s；开始检查并保存产物", time.Since(completionWait).Round(time.Millisecond)))
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
			if placeErr := a.placeFilesMode(s, &c, dir, t.Overwrite); placeErr != nil && e == nil {
				e = placeErr
			}
		} else if e == nil && (t.Overwrite || len(engine.Inventory(c, "").Paths) == 0) {
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
	log("完成阶段应用锁已释放；准备结束任务并归还下载名额")
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
		delete(a.activeInfo, t.ID)
		delete(a.credentialPaused, t.ID)
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
	state, _ := a.Credentials.Status(a.activeInfo[t.ID].Platform)
	credentialPaused := a.credentialPaused[t.ID] || errors.Is(taskErr, credentials.ErrUnavailable) || taskErr != nil && a.activeInfo[t.ID].UsesCookie && !state.Usable
	if errors.Is(taskErr, context.Canceled) || credentialPaused {
		current.Status = "queued"
		current.Error = "服务停止，等待继续执行"
		if credentialPaused {
			current.Error = "Cookie不可用，任务保留在平台队列；请更新Cookie后继续"
			if state.PlatformEnabled {
				current.Error = "已停用失效Cookie，等待以匿名方式重新执行"
			}
			if state.Usable {
				current.Error = "Cookie已更新，等待使用新配置继续执行"
			}
		}
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

// CookieChanged 使新上传凭据立即参与调度，停止携带旧版本的进程后继续原队列。
func (a *App) CookieChanged() { a.wakeScheduler() }
