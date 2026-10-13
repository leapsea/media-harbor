package service

import (
	"context"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"strings"
	"testing"
	"time"
)

// pipelineEngine 可停住生产者，观察消费者是否独立启动以及写通道是否阻塞。
type pipelineEngine struct {
	fakeEngine
	produced   chan int
	release    chan struct{}
	downloaded chan struct{}
	count      int
}

func (p pipelineEngine) Scan(ctx context.Context, _ domain.Subscription, _ int, _ func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	for i := 1; i <= p.count; i++ {
		if err := emit(domain.ScanResult{Items: []domain.Content{{RemoteID: fmt.Sprint(i), URL: "https://example.com/media"}}}); err != nil {
			return domain.ScanResult{}, err
		}
		p.produced <- i
	}
	if p.release != nil {
		select {
		case <-p.release:
			return domain.ScanResult{}, errors.New("分页失败")
		case <-ctx.Done():
			return domain.ScanResult{}, ctx.Err()
		}
	}
	return domain.ScanResult{}, nil
}
func (p pipelineEngine) Download(ctx context.Context, sub domain.Subscription, item domain.Content, options domain.YouTubeOptions, dir string, log func(string)) error {
	close(p.downloaded)
	return p.fakeEngine.Download(ctx, sub, item, options, dir, log)
}

// TestScanProducerBackpressure 覆盖内存通道和持久队列同时有界、取消解除阻塞。
func TestScanProducerBackpressure(t *testing.T) {
	p := pipelineEngine{count: config.ScanQueueCapacity*2 + 5, produced: make(chan int, config.ScanQueueCapacity*3)}
	a := testApp(t, p)
	sub := addSub(t, a, "bounded", "instagram")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.streamScan(ctx, sub, 0, false, func(string) {}, func(batch domain.ScanResult) error {
			for _, c := range batch.Items {
				if err := a.Repo.AddContentAndTask(sub.ID, c); err != nil {
					return err
				}
			}
			return nil
		})
		done <- err
	}()
	// 32 条持久队列、32 条通道缓冲和当前等待入队的一条；下一次写入必须等待。
	for i := 1; i <= config.ScanQueueCapacity*2+1; i++ {
		select {
		case n := <-p.produced:
			if n != i {
				t.Fatalf("produced=%d want=%d", n, i)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("生产者未到达容量边界")
		}
	}
	select {
	case n := <-p.produced:
		t.Fatalf("队列满仍继续生产：%d", n)
	case <-time.After(100 * time.Millisecond):
	}
	items, _ := a.Repo.Contents()
	if len(items) != config.ScanQueueCapacity {
		t.Fatalf("persistent=%d", len(items))
	}
	// 模拟一个消费者完成下载，持久容量释放后生产者必须恢复写入。
	a.mu.Lock()
	item := items[0]
	item.Status = "completed"
	_ = a.Repo.SaveContent(&item)
	tasks, _ := a.Repo.Tasks()
	for _, task := range tasks {
		if task.ContentID == item.ID {
			task.Status = "completed"
			_ = a.Repo.SaveTask(&task)
		}
	}
	a.mu.Unlock()
	select {
	case n := <-p.produced:
		if n != config.ScanQueueCapacity*2+2 {
			t.Fatalf("resumed=%d", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("释放容量后生产者未恢复")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未解除阻塞")
	}
}

// TestDownloadStartsBeforeScanEnds 即使平台并发为 1，生产者与消费者也能重叠。
// 后续分页错误保留已消费内容和成功下载，不撤销部分成果。
func TestDownloadStartsBeforeScanEnds(t *testing.T) {
	p := pipelineEngine{count: 1, produced: make(chan int, 1), release: make(chan struct{}), downloaded: make(chan struct{})}
	a := testApp(t, p)
	settings, _ := a.Repo.Settings()
	settings.YouTubeConcurrent = 1
	_ = a.SaveSettings(settings)
	sub := addSub(t, a, "overlap", "youtube")
	task, err := a.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.wg.Wait() }()
	if err = a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.produced:
	case <-time.After(3 * time.Second):
		t.Fatal("没有生产媒体")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		items, _ := a.Repo.Contents()
		if len(items) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("媒体未立即入队")
		}
		time.Sleep(time.Millisecond)
	}
	if err = a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.downloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("扫描未完成时没有开始下载")
	}
	current, _ := a.Repo.Task(task.ID)
	if current.Status != "running" {
		t.Fatalf("scan=%s", current.Status)
	}
	close(p.release)
	a.wg.Wait()
	current, _ = a.Repo.Task(task.ID)
	items, _ := a.Repo.Contents()
	if current.Status != "failed" || len(items) != 1 || items[0].Status != "completed" {
		t.Fatalf("scan=%+v items=%+v", current, items)
	}
}

// 调试范围外的积压没有消费者，不能占用扫描的背压容量。
func TestScanQueueIgnoresUnscheduledDebugTasks(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "debug-pending", "instagram")
	settings, _ := a.Repo.Settings()
	settings.Debug.TestItemLimit = 10
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10+config.ScanQueueCapacity; i++ {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := a.Repo.Tasks()
	for _, task := range selectedTasks(tasks, sub.ID, 10) {
		task.Status = "completed"
		if err := a.Repo.SaveTask(&task); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := a.waitScanQueue(ctx, sub.ID, domain.Content{RemoteID: "new"}, false, func(string) {}); err != nil {
		t.Fatalf("不可执行的历史积压阻塞扫描：%v", err)
	}
}

// 无法写入下载目录时，扫描不能无限等待一个不会启动的消费者。
func TestScanQueueReportsUnavailableConsumer(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "unwritable", "instagram")
	for i := 0; i < config.ScanQueueCapacity; i++ {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	a.directories[1].Writable = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := a.waitScanQueue(ctx, sub.ID, domain.Content{RemoteID: "new"}, false, func(string) {})
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("消费者不可执行时应显式报错：%v", err)
	}
}

// 消费者未释放容量时输出实际任务身份和阶段；取消仍解除背压等待。
func TestScanQueueReportsRunningConsumer(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "consumer-status", "instagram")
	for i := 0; i < config.ScanQueueCapacity; i++ {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	task.Status = "running"
	task.StartedAt = time.Now()
	task.Progress = []domain.TransferProgress{{Kind: "media", Status: "running", BytesDone: 512, BytesTotal: 1024, Percent: 50}}
	if err := a.Repo.SaveTask(&task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var line string
	err := a.waitScanQueue(ctx, sub.ID, domain.Content{RemoteID: "new"}, false, func(message string) { line = message; cancel() })
	if !errors.Is(err, context.Canceled) || !strings.Contains(line, "待执行 31 · 运行 1") || !strings.Contains(line, fmt.Sprintf("#%d", task.ID)) || !strings.Contains(line, "media 50.0%") {
		t.Fatalf("缺失消费者诊断：%s %v", line, err)
	}
}

// 持续生产超过容量上限时，真实调度循环必须反复消费并解除背压。
type drainingEngine struct{ pipelineEngine }

func (p drainingEngine) Download(ctx context.Context, sub domain.Subscription, item domain.Content, options domain.YouTubeOptions, dir string, log func(string)) error {
	return p.fakeEngine.Download(ctx, sub, item, options, dir, log)
}
func TestScanQueueDrainsAcrossCapacityBoundaries(t *testing.T) {
	count := config.ScanQueueCapacity*2 + 5
	p := drainingEngine{pipelineEngine{count: count, produced: make(chan int, count)}}
	a := testApp(t, p)
	sub := addSub(t, a, "continuous-consumers", "instagram")
	task, err := a.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	startEventScheduler(t, a)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, err := a.Repo.Task(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		counts, err := a.Repo.TaskStatusCounts()
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == "completed" && counts["completed"] == count+1 {
			return
		}
		if counts["failed"] > 0 {
			t.Fatalf("流水线执行失败：%v", counts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	tasks, _ := a.Repo.Tasks()
	t.Fatalf("跨越容量上限后流水线未继续消费：%+v", tasks)
}

// 应用锁无法取得时，诊断仍可读取“等待应用锁”，不会重复等待同一把锁。
func TestSchedulerPhaseVisibleWhileApplicationLockHeld(t *testing.T) {
	a := testApp(t, fakeEngine{})
	a.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- a.tick(context.Background()) }()
	defer func() {
		a.mu.Unlock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		phase := a.schedulerPhase.Load()
		if phase != nil && phase.Name == "等待应用锁" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("诊断无法读取被锁阻塞的调度阶段")
}
