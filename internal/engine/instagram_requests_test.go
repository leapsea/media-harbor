package engine

import (
	"context"
	"encoding/json"
	"errors"
	"media-harbor/internal/domain"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestInstagramSharedInterval 验证扫描和下载解析共用时钟，取消不会泄漏许可。
func TestInstagramSharedInterval(t *testing.T) {
	r := newInstagramRequests()
	r.interval = 30 * time.Millisecond
	log := func(string) {}
	release, err := r.wait(context.Background(), log, "scan")
	// 此时只持有响应头阶段的许可，模拟请求结束后再启动另一个阶段。
	if err != nil {
		t.Fatal(err)
	}
	first := time.Now()
	release()
	// 后续媒体请求必须等待扫描的请求间隔，而非使用另一个进程时钟。
	release, err = r.wait(context.Background(), log, "media")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(first) < 25*time.Millisecond {
		t.Fatal("没有共享请求间隔")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.wait(ctx, log, "avatar")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	// 取消等待者后仍可继续请求，不能永久占用平台许可。
	r.interval = 0
	// 已取消的调用即使碰到空闲许可也不能启动请求或推进时钟。
	_, err = r.wait(ctx, log, "scan")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release, err = r.wait(context.Background(), log, "scan")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// TestInstagramSharedCooldown 验证请求暂停影响另一个执行阶段，并遵循响应头。
func TestInstagramSharedCooldown(t *testing.T) {
	r := newInstagramRequests()
	r.interval = 0
	r.fallback = time.Second
	left := r.rateLimited("0.06")
	// 服务端较短的 Retry-After 应覆盖默认值，不能一律使用一分钟。
	if left > 100*time.Millisecond || left < 50*time.Millisecond {
		t.Fatal(left)
	}
	started := time.Now()
	release, err := r.wait(context.Background(), func(string) {}, "avatar")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if time.Since(started) < 50*time.Millisecond {
		t.Fatal("头像请求越过统一冷却")
	}
	// HTTP 日期与秒数形式都支持；等待时刻只能延长，不能被其他响应缩短。
	deadline := time.Now().Add(10 * time.Second).UTC().Truncate(time.Second)
	r.rateLimited(deadline.Format(http.TimeFormat))
	old := r.cooldown
	// 后来的较短等待不能缩短已经记录的冷却期限。
	r.rateLimited("0")
	if !old.Equal(r.cooldown) || r.cooldown.Before(deadline) {
		t.Fatal(r.cooldown, deadline)
	}
}

// TestInstagramControllerCancellation 进程退出时归还仍持有的许可并关闭控制端口。
func TestInstagramControllerCancellation(t *testing.T) {
	r := newInstagramRequests()
	r.interval = 0
	_, end, err := r.begin(context.Background(), "scan", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 SDK 已获准但尚未返回响应就被取消，不能阻塞下一任务。
	release, err := r.wait(context.Background(), func(string) {}, "scan")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	for _, op := range r.ops {
		op.leases["pending"] = release
	}
	r.mu.Unlock()
	end()
	// 清理后保留相同调度器，验证新批次能继续取得许可。
	if r.server != nil || len(r.ops) != 0 {
		t.Fatal("会话结束仍保留控制端口")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err = r.wait(ctx, func(string) {}, "media")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

// TestInstagramWebPost 保留超大整数身份，拒绝把推荐对象当作当前帖子。
func TestInstagramWebPost(t *testing.T) {
	page := `<script>invalid</script><script>{"route":{"shortcode":"target"},"recommended":{"code":"other","pk":1,"user":{},"image_versions2":{}},"post":{"code":"target","pk":123456789012345678,"user":{"username":"author"},"image_versions2":{"candidates":[]},"carousel_media":[{},{}]}}</script>`
	post := instagramWebPost(page, "target")
	// JSON 大整数与轮播数组必须完整保留给原下载器。
	if post == nil || post["pk"] != json.Number("123456789012345678") || len(post["carousel_media"].([]any)) != 2 {
		t.Fatal(post)
	}
	if instagramWebPost(page, "missing") != nil {
		t.Fatal("解析了其他帖子")
	}
}

// TestInstagramGoByteProgress 验证真实计数进入统一进度协议，原始事件不泄漏到日志。
func TestInstagramGoByteProgress(t *testing.T) {
	var events []domain.TransferProgress
	log := instagramByteLog(func(line string) {
		if !strings.HasPrefix(line, ProgressPrefix) {
			t.Fatal(line)
		}
		var event domain.TransferProgress
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, ProgressPrefix)), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	})
	log(`@instagram-bytes {"done":512,"total":1024}`)
	log(`@instagram-bytes {"done":1024,"total":1024}`)
	// 最后一条字节事件仍未提交文件与任务状态，不提前显示整个任务完成。
	if len(events) != 2 || events[0].Percent != 50 || events[1].Percent != 99.9 || events[1].Status != "running" {
		t.Fatal(events)
	}
}
