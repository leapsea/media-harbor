package service

import (
	"context"
	"media-harbor/internal/domain"
	"testing"
	"time"
)

// slotEngine 通过可控完成事件观察补位，不依赖平台响应速度。
type slotEngine struct {
	fakeEngine
	started chan string
	release chan struct{}
}

// childSlotEngine 的视频阶段立即完成，附属阶段必须显式结束后才能交还名额。
type childSlotEngine struct {
	fakeEngine
	started  chan string
	children chan string
	release  chan struct{}
}

func (e childSlotEngine) Download(ctx context.Context, s domain.Subscription, c domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	e.started <- c.RemoteID
	if err := e.fakeEngine.Download(ctx, s, c, o, dir, log); err != nil {
		return err
	}
	e.children <- c.RemoteID
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
		return nil
	}
}

func TestChildTasksHoldVideoSlot(t *testing.T) {
	e := childSlotEngine{started: make(chan string, 2), children: make(chan string, 2), release: make(chan struct{}, 1)}
	a := testApp(t, e)
	settings, _ := a.Repo.Settings()
	settings.YouTubeConcurrent = 1
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s := addSub(t, a, "children", "youtube")
	for _, id := range []string{"one", "two"} {
		if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: id}); err != nil {
			t.Fatal(err)
		}
	}
	startEventScheduler(t, a)
	select {
	case <-e.children:
	case <-time.After(3 * time.Second):
		t.Fatal("附属任务未启动")
	}
	<-e.started
	select {
	case <-e.started:
		t.Fatal("附属任务尚未结束就启动下一个视频")
	default:
	}
	e.release <- struct{}{}
	select {
	case <-e.started:
	case <-time.After(3 * time.Second):
		t.Fatal("附属任务结束后未释放名额")
	}
}

func (e slotEngine) Download(ctx context.Context, s domain.Subscription, c domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	e.started <- c.RemoteID
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.release:
		return e.fakeEngine.Download(ctx, s, c, o, dir, log)
	}
}

// startEventScheduler 禁用定时信号，保证后续执行只能由队列事件触发。
func startEventScheduler(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); a.runScheduler(ctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestCompletionImmediatelyRefillsSlots(t *testing.T) {
	e := slotEngine{started: make(chan string, 3), release: make(chan struct{}, 1)}
	a := testApp(t, e)
	s := addSub(t, a, "refill", "youtube")
	for _, id := range []string{"one", "two", "three"} {
		if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: id}); err != nil {
			t.Fatal(err)
		}
	}
	startEventScheduler(t, a)
	for i := 0; i < 2; i++ {
		select {
		case <-e.started:
		case <-time.After(3 * time.Second):
			t.Fatal("启动未填满两个视频名额")
		}
	}
	select {
	case <-e.started:
		t.Fatal("超过平台并发上限")
	default:
	}
	e.release <- struct{}{}
	select {
	case <-e.started:
	case <-time.After(3 * time.Second):
		t.Fatal("完成后未通过事件立即补位")
	}
}

func TestParsedContentImmediatelyStartsDownload(t *testing.T) {
	e := pipelineEngine{count: 1, produced: make(chan int, 1), release: make(chan struct{}), downloaded: make(chan struct{})}
	// 扫描在产出一条内容后等待取消，下载必须在扫描结束前启动。
	a := testApp(t, e)
	s := addSub(t, a, "parsed", "youtube")
	startEventScheduler(t, a)
	scan, err := a.QueueScan(s.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.downloaded:
	case <-time.After(3 * time.Second):
		t.Fatal("解析入队没有立即触发下载")
	}
	current, err := a.Repo.Task(scan.ID)
	if err != nil || current.Status != "running" {
		t.Fatal(current, err)
	}
}
