package service

import (
	"context"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
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
