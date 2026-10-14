package engine

import (
	"context"
	"errors"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"testing"
	"time"
)

// 请求窗口统计实际发起时刻，失败和重试也不能使用额外预算。
func TestProducerSlidingWindowAndLiveSettings(t *testing.T) {
	// 缩短窗口观察阻塞，无需真实账号或一分钟等待。
	r := newInstagramRequests()
	r.interval, r.window, r.maximum = 0, 70*time.Millisecond, 2
	for i := 0; i < 2; i++ {
		release, err := r.wait(context.Background(), func(string) {}, "metadata")
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	// 第三个请求必须等待最早请求移出窗口。
	started := time.Now()
	release, err := r.wait(context.Background(), func(string) {}, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if time.Since(started) < 60*time.Millisecond {
		t.Fatal("越过滑动窗口")
	}
	// 保存更宽松的预算立即唤醒等待者，既有请求历史继续保留。
	r.configure(domain.ProducerSettings{WindowSeconds: 60, MaxRequests: 1})
	// 等待线程在修改前后启动都必须使用最新预算。
	done := make(chan error, 1)
	go func() {
		release, err := r.wait(context.Background(), func(string) {}, "avatar")
		if err == nil {
			release()
		}
		done <- err
	}()
	r.configure(domain.ProducerSettings{WindowSeconds: 60, MaxRequests: 10})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("设置更新没有解除等待")
	}
	if len(r.history) != 2 {
		t.Fatalf("请求历史被重置或重复：%v", r.history)
	}
}

// 单生产者与多个下载消费者彼此独立，跨平台也不会争抢许可。
func TestPlatformProducerAndConsumerIndependence(t *testing.T) {
	c := NewCLI(t.TempDir(), nil)
	s := config.Defaults()
	// 不同平台上限不能变成一个全局共享信号量。
	s.InstagramConcurrent = 2
	s.YouTubeConcurrent = 1
	c.Configure(s)
	// 生产者持有执行权时，两名消费者仍然可以传输。
	p := c.pipeline("instagram")
	producer, err := p.acquireProducer(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer producer()
	first, err := p.acquireConsumer(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := p.acquireConsumer(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	// 第二名生产者与第三名消费者碰到各自的上限。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.acquireProducer(ctx, func(string) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("同时启动第二个解析器", err)
	}
	if _, err := p.acquireConsumer(ctx, func(string) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("超过下载并发", err)
	}
	// Instagram 满载不妨碍 YouTube 元数据处理。
	other, err := c.pipeline("youtube").acquireProducer(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	other()
	// 正在等待的下载可以取消，不能泄漏名额。
	first()
	next, err := p.acquireConsumer(context.Background(), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	next()
}

// 运行参数确实作用于缓存，修改有效期不改变本地媒体索引。
func TestInstagramCacheSettingsApply(t *testing.T) {
	c := NewCLI(t.TempDir(), nil)
	item := domain.Content{URL: "post", RemoteID: "one", Asset: 1}
	c.instagram.put(domain.ScanResult{Items: []domain.Content{item}, MediaSources: map[string]domain.MediaSource{"one": {URL: "media"}}})
	// 只调整地址快照时间，不创建或修改本地媒体文件。
	c.instagram.mu.Lock()
	entry := c.instagram.entries[instagramSourceKey("post", 1)]
	entry.at = time.Now().Add(-2 * time.Minute)
	c.instagram.entries[instagramSourceKey("post", 1)] = entry
	c.instagram.mu.Unlock()
	if _, ok := c.instagram.get(item); !ok {
		t.Fatal("默认有效期失效")
	}
	s := config.Defaults()
	// 两分钟前的地址仍在默认窗口内，缩短设置后立即过期。
	s.Pipeline.InstagramSourceCacheTTLMinutes = 1
	s.Pipeline.InstagramSourceCacheCapacity = 1
	c.Configure(s)
	if _, ok := c.instagram.get(item); ok {
		t.Fatal("有效期未生效")
	}
}

// 媒体连接不占解析预算，但仍尊重真实429冷却且能立即取消。
func TestMediaIgnoresProducerBudgetButRespectsCooldown(t *testing.T) {
	r := newInstagramRequests()
	r.configure(domain.ProducerSettings{WindowSeconds: 60, MaxRequests: 1, MinIntervalSeconds: 6, CooldownSeconds: 60})
	release, err := r.wait(context.Background(), func(string) {}, "scan")
	if err != nil {
		t.Fatal(err)
	}
	release()
	// 解析窗口已满也不能阻塞一个正常CDN下载。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.waitMedia(ctx, func(string) {}); err != nil {
		t.Fatal(err)
	}
	// 服务端更短的期限应有效，避免SDK再睡默认一分钟。
	r.rateLimited("0.05")
	started := time.Now()
	if err := r.waitMedia(context.Background(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 40*time.Millisecond {
		t.Fatal("媒体越过429冷却")
	}
	// 取消只解除当前下载等待，不能重置平台期限或泄漏许可。
	r.rateLimited("1")
	waitCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if err := r.waitMedia(waitCtx, func(string) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
