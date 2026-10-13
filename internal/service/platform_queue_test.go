package service

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/storage"
	"strings"
	"sync"
	"testing"
	"time"
)

// authorQueueEngine 将扫描产出、扫描结束和下载结束分别控制，验证真实状态转换。
type authorQueueEngine struct {
	fakeEngine
	scans                            chan int64
	downloads                        chan int64
	emit, finishScan, finishDownload sync.Map
}

func newAuthorQueueEngine() *authorQueueEngine {
	// 事件缓冲覆盖本测试的小批媒体，不因测试观察速度影响调度。
	return &authorQueueEngine{scans: make(chan int64, 20), downloads: make(chan int64, 20)}
}
func (e *authorQueueEngine) prepare(id int64) {
	// 门控通道只在测试中使用，取消 Context 始终可以中断等待。
	for _, gates := range []*sync.Map{&e.emit, &e.finishScan, &e.finishDownload} {
		gates.Store(id, make(chan struct{}))
	}
}
func waitAuthorGate(ctx context.Context, gates *sync.Map, id int64) error {
	value, _ := gates.Load(id)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-value.(chan struct{}):
		return nil
	}
}
func releaseAuthorGate(gates *sync.Map, id int64) {
	value, _ := gates.Load(id)
	close(value.(chan struct{}))
}
func (e *authorQueueEngine) Scan(ctx context.Context, sub domain.Subscription, _ int, _ func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	e.scans <- sub.ID
	if err := waitAuthorGate(ctx, &e.emit, sub.ID); err != nil {
		return domain.ScanResult{}, err
	}
	// 四条下载在其他博主入队后才产生，用于排除按媒体 ID 错误交接的实现。
	for i := range 4 {
		if err := emit(domain.ScanResult{Items: []domain.Content{{RemoteID: fmt.Sprint(i), URL: "https://example.com/media"}}}); err != nil {
			return domain.ScanResult{}, err
		}
	}
	return domain.ScanResult{}, waitAuthorGate(ctx, &e.finishScan, sub.ID)
}
func (e *authorQueueEngine) Download(ctx context.Context, sub domain.Subscription, item domain.Content, options domain.YouTubeOptions, dir string, log func(string)) error {
	e.downloads <- sub.ID
	if err := waitAuthorGate(ctx, &e.finishDownload, sub.ID); err != nil {
		return err
	}
	return e.fakeEngine.Download(ctx, sub, item, options, dir, log)
}

func expectAuthorEvent(t *testing.T, events <-chan int64, id int64) {
	// 观察实际进入引擎的配置，避免只依赖预先写入的 running 状态。
	t.Helper()
	select {
	case got := <-events:
		if got != id {
			t.Fatalf("启动配置 %d，预期 %d", got, id)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("配置 %d 未启动", id)
	}
}
func expectTaskState(t *testing.T, a *App, id int64, state string) {
	// 等待领域状态提交，固定上限仅用作测试失败边界。
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := a.Repo.Task(id)
		if err == nil && task.Status == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	task, err := a.Repo.Task(id)
	t.Fatalf("期望 %s：%+v %v", state, task, err)
}

// TestPlatformAuthorQueueKeepsDownloads 验证 A 扫描和下载结束前 B 一直排队，YouTube 同时执行。
func TestPlatformAuthorQueueKeepsDownloads(t *testing.T) {
	// 两个平台共享真实数据库，Instagram 下载额度限定为两个线程。
	e := newAuthorQueueEngine()
	a := testApp(t, e)
	settings, _ := a.Repo.Settings()
	settings.InstagramConcurrent = 2
	settings.Debug.TestItemLimit = 10
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	first := addSub(t, a, "instagram-a", "instagram")
	second := addSub(t, a, "instagram-b", "instagram")
	youtube := addSub(t, a, "youtube-a", "youtube")
	for _, sub := range []domain.Subscription{first, second, youtube} {
		e.prepare(sub.ID)
	}
	// A 先占平台，B 与 YouTube 随后到达；B 不得借用空闲扫描额度。
	scan, _ := a.QueueScan(first.ID, "full")
	startEventScheduler(t, a)
	expectAuthorEvent(t, e.scans, first.ID)
	queued, _ := a.QueueScan(second.ID, "full")
	_, _ = a.QueueScan(youtube.ID, "full")
	expectAuthorEvent(t, e.scans, youtube.ID)
	expectTaskState(t, a, queued.ID, "queued")
	// B 已入队后再释放 A 的媒体，后产生的下载也属于 A 当前批次。
	releaseAuthorGate(&e.emit, first.ID)
	for range 2 {
		expectAuthorEvent(t, e.downloads, first.ID)
	}
	releaseAuthorGate(&e.finishScan, first.ID)
	expectTaskState(t, a, scan.ID, "completed")
	expectTaskState(t, a, queued.ID, "queued")
	// 两个下载线程占满时，其余两个仍排队；扫描结束不释放博主执行权。
	groups, _ := a.TaskGroups()
	for _, group := range groups {
		if group.SubscriptionID == first.ID && (group.Counts["running"] != 2 || group.Counts["queued"] != 2) {
			t.Fatal(group)
		}
	}
	releaseAuthorGate(&e.finishDownload, first.ID)
	// 完成全部下载后立即唤醒 B，平台交接不依赖定时轮询。
	expectAuthorEvent(t, e.scans, second.ID)
	// 交接通过完成事件触发，测试没有给调度器任何定时 tick。
	contents, _ := a.Repo.Contents()
	for _, item := range contents {
		if item.SubscriptionID == first.ID && item.Status != "completed" {
			t.Fatal(item)
		}
	}
}

// TestPlatformDueScanWaits 定时到期只入队，不因空闲线程抢占另一个博主。
func TestPlatformDueScanWaits(t *testing.T) {
	// 当前采集尚未返回任何媒体，平台仍属于它，空余下载线程不能放行 B。
	e := newAuthorQueueEngine()
	a := testApp(t, e)
	first := addSub(t, a, "active", "instagram")
	second := addSub(t, a, "due", "instagram")
	e.prepare(first.ID)
	e.prepare(second.ID)
	_, _ = a.QueueScan(first.ID, "full")
	startEventScheduler(t, a)
	expectAuthorEvent(t, e.scans, first.ID)
	second.LastScan = time.Now().Add(-time.Hour)
	// 直接设置已到期快照，避免等待真实定时间隔或启动全量历史。
	second.NextRun = time.Now().Add(-time.Minute)
	second.Schedule = "@every 1h"
	if err := a.Repo.SaveSubscription(&second); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// 多次调度不能重复生成同一个到期扫描。
	tasks, _ := a.Repo.SubscriptionTasks(second.ID)
	if len(tasks) != 1 || tasks[0].Status != "queued" {
		t.Fatal(tasks)
	}
	log, err := a.GroupLog(second.ID)
	if err != nil || !containsQueueLog(log) {
		t.Fatal(log, err)
	}
}
func containsQueueLog(log string) bool { return strings.Contains(log, "平台排队") }

// TestPlatformFutureBatchDoesNotJumpQueue 当前 A 的下一轮排在已等待的 B 后面。
func TestPlatformFutureBatchDoesNotJumpQueue(t *testing.T) {
	// 已持有执行权的 A 与等待的 B 使用可比较的入队时间。
	a := testApp(t, fakeEngine{})
	first := addSub(t, a, "first", "instagram")
	second := addSub(t, a, "second", "instagram")
	base := time.Now().Add(-10 * time.Second)
	run := domain.PlatformRun{Platform: "instagram", SubscriptionID: first.ID, ScanTaskID: 1, StartedAt: base}
	if err := a.Repo.SavePlatformRun(run); err != nil {
		t.Fatal(err)
	}
	// 批次来源可让 A 的后来下载继续运行，下一轮扫描不享受这个执行权。
	tasks := []domain.Task{{ID: 2, SubscriptionID: second.ID, Status: "queued", CreatedAt: base.Add(time.Second)},
		{ID: 3, SubscriptionID: first.ID, ContentID: 10, Kind: "download", SourceTaskID: 1, Status: "queued", CreatedAt: base.Add(2 * time.Second)},
		{ID: 4, SubscriptionID: first.ID, Status: "queued", CreatedAt: base.Add(3 * time.Second)}}
	subs := map[int64]domain.Subscription{first.ID: first, second.ID: second}
	runs, err := a.platformQueueLocked(tasks, subs, nil, 0)
	if err != nil || runs["instagram"].SubscriptionID != first.ID || belongsToPlatformRun(tasks[2], runs["instagram"]) {
		t.Fatal(runs, err)
	}
	tasks[1].Status = "completed"
	// 当前下载结束后，A 的下一轮扫描不能早于之前入队的 B。
	runs, err = a.platformQueueLocked(tasks, subs, nil, 0)
	if err != nil || runs["instagram"].SubscriptionID != second.ID {
		t.Fatal(runs, err)
	}
}

// TestPlatformBacklogAllowsNewScanChildren 旧下载和已排队扫描共存时，扫描子任务不能被阻塞。
func TestPlatformBacklogAllowsNewScanChildren(t *testing.T) {
	// 队首是旧下载，同时已有一轮新的扫描；两者属于这次已有工作集合。
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "backlog", "instagram")
	base := time.Now().Add(-time.Second)
	tasks := []domain.Task{{ID: 1, SubscriptionID: sub.ID, ContentID: 10, Kind: "download", SourceTaskID: 9, Status: "queued", CreatedAt: base},
		{ID: 2, SubscriptionID: sub.ID, Status: "queued", CreatedAt: base.Add(time.Millisecond)}}
	runs, err := a.platformQueueLocked(tasks, map[int64]domain.Subscription{sub.ID: sub}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 下载的来源扫描与当前排队扫描不同，两个来源都要保留在本批次中。
	child := domain.Task{SubscriptionID: sub.ID, ContentID: 11, SourceTaskID: 2, CreatedAt: time.Now()}
	if !belongsToPlatformRun(child, runs["instagram"]) {
		t.Fatal("本批扫描产生的下载被错误阻塞", runs)
	}
}

// TestPlatformRetryUsesArrivalTime 旧任务重试不能依靠原始 ID 插到新队列前面。
func TestPlatformRetryUsesArrivalTime(t *testing.T) {
	// 原始任务 ID 更小，但它的本次重试时间晚于 B 的入队时间。
	a := testApp(t, fakeEngine{block: true})
	first := addSub(t, a, "retry", "youtube")
	second := addSub(t, a, "waiting", "youtube")
	old, _ := a.QueueScan(first.ID, "full")
	old.Status = "failed"
	if err := a.Repo.SaveTask(&old); err != nil {
		t.Fatal(err)
	}
	waiting, _ := a.QueueScan(second.ID, "full")
	if err := a.RetryTask(old.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.wg.Wait() }()
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	expectTaskState(t, a, waiting.ID, "running")
	expectTaskState(t, a, old.ID, "queued")
}

// TestPlatformRunSurvivesRestart 扫描已结束但下载未结束时，重启仍先继续 A。
func TestPlatformRunSurvivesRestart(t *testing.T) {
	// B 的扫描 ID 小于 A 的剩余下载，单纯按任务 ID 恢复会错误地抢占。
	a := testApp(t, fakeEngine{block: true})
	first := addSub(t, a, "resume-a", "instagram")
	second := addSub(t, a, "resume-b", "instagram")
	scan, _ := a.QueueScan(first.ID, "full")
	run := domain.PlatformRun{Platform: "instagram", SubscriptionID: first.ID, ScanTaskID: scan.ID, StartedAt: time.Now()}
	if err := a.Repo.SavePlatformRun(run); err != nil {
		t.Fatal(err)
	}
	waiting, _ := a.QueueScan(second.ID, "full")
	if err := a.Repo.AddContentAndTask(first.ID, domain.Content{RemoteID: "remaining", SourceTaskID: scan.ID}); err != nil {
		t.Fatal(err)
	}
	scan.Status = "completed"
	if err := a.Repo.SaveTask(&scan); err != nil {
		t.Fatal(err)
	}
	// 关闭并重开真实数据库，不能只用同一个 App 的内存状态证明恢复。
	_ = a.Repo.(*storage.Store).Close()
	db, err := storage.Open(a.Config.DataDir+"/media.db", config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resumed := New(a.Config, db, fakeEngine{block: true})
	// 全新的服务实例验证恢复；保留的执行权无需原来的 goroutine 或缓存。
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); resumed.wg.Wait() }()
	if err = resumed.tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, _ := db.SubscriptionTasks(first.ID)
	for _, task := range tasks {
		if task.ContentID != 0 && task.Status != "running" {
			t.Fatal(task)
		}
	}
	expectTaskState(t, resumed, waiting.ID, "queued")
}

// TestPlatformDeleteWaitsForProcessExit 删除配置不等于其引擎已退出，下一位仍需等待。
func TestPlatformDeleteWaitsForProcessExit(t *testing.T) {
	// 引擎收到取消后仍等待清理，以模拟实际进程退出慢于记录删除。
	e := delayedStopEngine{started: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{})}
	a := testApp(t, e)
	first := addSub(t, a, "delete-a", "instagram")
	second := addSub(t, a, "delete-b", "instagram")
	_, _ = a.QueueScan(first.ID, "full")
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); close(e.release); a.wg.Wait() }()
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	<-e.started
	waiting, _ := a.QueueScan(second.ID, "full")
	if err := a.DeleteSubscription(first.ID); err != nil {
		t.Fatal(err)
	}
	<-e.stopping
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	// 记录已删除，判断占用必须来自实际进程归属而非任务表查询。
	expectTaskState(t, a, waiting.ID, "queued")
}
