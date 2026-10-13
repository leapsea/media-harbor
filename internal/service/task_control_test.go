package service

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"testing"
	"time"
)

// delayedStopEngine 模拟引擎收到取消后仍在清理，取消接口无需等待退出。
type delayedStopEngine struct {
	fakeEngine
	started  chan struct{}
	stopping chan struct{}
	release  chan struct{}
}

func (e delayedStopEngine) Scan(ctx context.Context, _ domain.Subscription, _ int, _ func(string), _ func(domain.ScanResult) error) (domain.ScanResult, error) {
	// started 和 stopping 分别区分引擎已启动与收到取消，release 控制真正退出。
	close(e.started)
	<-ctx.Done()
	close(e.stopping)
	<-e.release
	return domain.ScanResult{}, ctx.Err()
}

// TestGroupCancelDoesNotWaitForEngineExit 验证接口落库返回与引擎实际退出的边界。
func TestGroupCancelDoesNotWaitForEngineExit(t *testing.T) {
	// 数据库使用真实实现；只有外部引擎退出速度由测试控制。
	e := delayedStopEngine{started: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{})}
	a := testApp(t, e)
	s := addSub(t, a, "cancel-slow-engine", "youtube")
	task, err := a.QueueScan(s.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// 始终释放引擎并等待后台退出，避免测试泄漏执行上下文。
	defer func() { cancel(); close(e.release); a.wg.Wait() }()
	if err = a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.started:
	case <-time.After(time.Second):
		t.Fatal("引擎未启动")
	}
	// release 尚未关闭；接口能返回即证明没有把引擎清理计入响应等待。
	count, err := a.GroupAction(s.ID, "cancel")
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	select {
	case <-e.stopping:
	case <-time.After(time.Second):
		t.Fatal("没有发出取消信号")
	}
	current, _ := a.Repo.Task(task.ID)
	if current.Status != "cancelled" {
		t.Fatal(current)
	}
	// 收到成功提示后仍尊重实际停止过程，不允许并发启动同一任务。
	if _, err = a.GroupAction(s.ID, "retry"); err == nil {
		t.Fatal("仍在停止的引擎不能立即重试")
	}
}

// 故障只注入取消事务，其他查询继续使用真实数据库。
type cancelFailureRepository struct{ domain.Repository }

func (cancelFailureRepository) CancelSubscriptionTasks(int64) ([]int64, error) {
	return nil, errors.New("取消事务失败")
}

// TestGroupCancelFailureDoesNotStopEngine 失败的持久化不能被误报成已取消。
func TestGroupCancelFailureDoesNotStopEngine(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "cancel-failure", "youtube")
	task, err := a.QueueScan(s.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.active[task.ID] = cancel
	// 未提交的取消不能终止上下文，也不能改变持久任务状态。
	a.Repo = cancelFailureRepository{a.Repo}
	if count, err := a.GroupAction(s.ID, "cancel"); err == nil || count != 0 {
		t.Fatal(count, err)
	}
	if ctx.Err() != nil {
		t.Fatal("事务失败却停止引擎")
	}
	current, _ := a.Repo.Task(task.ID)
	if current.Status != "queued" {
		t.Fatal(current)
	}
}
