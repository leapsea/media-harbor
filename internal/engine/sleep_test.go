package engine

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"testing"
	"time"
)

// engineSleepNow 生成包含当前时刻的窗口，避免依赖测试执行时区。
func engineSleepNow() domain.SleepSettings {
	now := time.Now().UTC()
	return domain.SleepSettings{Enabled: true, Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), Timezone: "UTC"}
}

// TestSleepPausesRequestsButDrainsMedia 验证在途生产请求等待，媒体不等待休眠。
func TestSleepPausesRequestsButDrainsMedia(t *testing.T) {
	r := newInstagramRequests()
	r.configure(domain.ProducerSettings{WindowSeconds: 60, MaxRequests: 20})
	r.configureSleep(engineSleepNow())
	// 日志事件证明请求已进入等待，结果通道证明尚未获准发送。
	entered, done := make(chan struct{}, 1), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		release, err := r.wait(ctx, func(string) {
			select {
			case entered <- struct{}{}:
			default:
			}
		}, "metadata")
		if err == nil {
			release()
		}
		done <- err
	}()
	// 使用阶段事件同步测试，不依靠随机等待猜测请求时序。
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("生产者没有进入休眠")
	}
	select {
	case err := <-done:
		t.Fatal("休眠请求被执行", err)
	default:
	}
	// 已开始的媒体会话只受429约束，仍能完成当前条目。
	if err := r.waitMedia(ctx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// 热关闭应唤醒原请求而非重置滑动窗口或重建引擎。
	r.configureSleep(domain.SleepSettings{})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭休眠没有唤醒")
	}
}

// TestSleepConsumerCancellation 新消费者和生产者启动都能被即时取消。
func TestSleepConsumerCancellation(t *testing.T) {
	p := newPlatformPipeline(newInstagramRequests(), 1)
	p.requests.configureSleep(engineSleepNow())
	for _, acquire := range []func(context.Context, func(string)) (func(), error){p.acquireProducer, p.acquireConsumer} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		release, err := acquire(ctx, func(string) {})
		cancel()
		if release != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	// 超时后两个名额均为空，防止后续恢复被残留许可阻挡。
	if len(p.producer) != 0 || p.consumers != 0 {
		t.Fatal("休眠取消后仍占有名额")
	}
}

// TestMediaScopeDrainsStages 同一媒体的附件复用名额，休眠只阻止后续条目。
func TestMediaScopeDrainsStages(t *testing.T) {
	c := NewCLI(t.TempDir(), nil)
	p := c.pipeline("youtube")
	scope := &mediaConsumerScope{}
	ctx := context.WithValue(context.Background(), mediaConsumerKey{}, scope)
	if scope.release != nil || p.consumers != 0 {
		t.Fatal("解析前已占下载名额")
	}
	release, err := c.consumer(ctx, "youtube", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	release()
	defer scope.release()
	p.requests.configureSleep(engineSleepNow())
	// 进入休眠后的附件无需申请另一个名额，已经运行的媒体继续收尾。
	release, err = c.consumer(ctx, "youtube", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	release()
	if p.consumers != 1 {
		t.Fatal("附件另占下载名额", p.consumers)
	}
	// 另一个条目无法借用当前条目的上下文许可。
	next, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = c.consumer(next, "youtube", func(string) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("新条目越过休眠", err)
	}
}
