package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// instagramFixture 保留真实 gallery-dl 与 HTTP 下载器，只用本地服务器代替远端。
// python 包未安装时明确跳过，容器验证必须额外覆盖这条实际执行路径。
func instagramFixture(t *testing.T, expire bool, status int) (CLI, string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil || exec.Command(python, "-c", "import gallery_dl").Run() != nil {
		t.Skip("未安装 gallery-dl")
	}
	var metadata, media atomic.Int32
	// 帖子请求与媒体请求分别统计，能够检测隐藏的重复解析。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			n := metadata.Add(1)
			path := "/media/"
			if expire && n == 1 {
				path = "/expired/"
			}
			// 返回两个轮播资源，不通过测试触发真实帖子接口。
			_ = json.NewEncoder(w).Encode([]string{path + "1.gif", path + "2.gif"})
			return
		}
		// 模拟 CDN 对签名地址和媒体头的校验，HTTP 403 不能被视为已完成。
		media.Add(1)
		if r.Header.Get("X-Media-Fixture") != "preserved" {
			t.Error("媒体请求头未复用")
		}
		// 失效路径只在第一次解析中返回，第二次解析必须得到新的可用地址。
		if strings.HasPrefix(r.URL.Path, "/expired/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write([]byte("GIF89a" + strings.Repeat("x", 1024)))
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	// 工作文件和解释器包装都在测试临时目录中，与真实队列隔离。
	bin := filepath.Join(root, "bin")
	if err = os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	// 包装解释器仅替换提取器来源，执行的缓存、进度与下载代码仍来自正式实现。
	wrapper := fmt.Sprintf(`#!%s
import json,re,sys,urllib.request
from gallery_dl import extractor
from gallery_dl.extractor.common import Extractor,Message
origin=%q
class Fixture(Extractor):
    category='instagram'
    subcategory='fixture'
    directory_fmt=()
    filename_fmt='{post_id}_{num}.{extension}'
    def items(self):
        with urllib.request.urlopen(origin+'/metadata') as response:
            paths=json.load(response)
        for index,path in enumerate(paths,1):
            data={'post_id':'fixture','num':index,'extension':'gif','post_url':self.url,
                  '_http_headers':{'X-Media-Fixture':'preserved'}}
            yield Message.Directory,'',data
            yield Message.Url,origin+path,data
extractor.find=lambda address: Fixture(re.match('.*',address))
code=sys.argv[3]
sys.argv=['gallery-dl']+sys.argv[4:]
exec(code)
`, python, server.URL)
	if err = os.WriteFile(filepath.Join(bin, "python3"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// 429 测试只验证不追加帖子解析，关闭素材下载器重试以避免真实一分钟等待。
	if status != http.StatusOK {
		if err = os.WriteFile(filepath.Join(root, "gallery-dl.json"), []byte(`{"downloader":{"http":{"retries":0,"sleep-429":0}}}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return NewCLI(root, nil), "https://www.instagram.com/p/fixture/", &metadata, &media
}

// TestInstagramScanSourcesAvoidPostRequests 验证扫描后两条轮播下载不再重复解析帖子。
func TestInstagramScanSourcesAvoidPostRequests(t *testing.T) {
	cli, address, metadata, media := instagramFixture(t, false, http.StatusOK)
	sub := domain.Subscription{Platform: "instagram", URL: address}
	items := []domain.Content{}
	// 保留真实扫描协议；只通过 emit 收集用户实际会入队的媒体实体。
	_, err := cli.Scan(context.Background(), sub, 10, func(line string) { t.Log(line) }, func(batch domain.ScanResult) error { items = append(items, batch.Items...); return nil })
	if err != nil || len(items) != 2 {
		t.Fatal(len(items), err)
	}
	for _, item := range items {
		// 每个资源拥有独立工作目录，缓存复用不会合并用户下载条目。
		dir := filepath.Join(t.TempDir(), "work")
		if err = cli.Download(context.Background(), sub, item, domain.YouTubeOptions{}, dir, func(line string) { t.Log(line) }); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(dir, fmt.Sprintf("fixture_%d.gif", item.Asset))); err != nil {
			t.Fatal(err)
		}
		// 隐藏快照不应混入可归位的媒体文件。
		if files := Inventory(item, dir).Paths; len(files) != 1 {
			t.Fatal(files)
		}
	}
	if metadata.Load() != 1 || media.Load() != 2 {
		t.Fatalf("metadata=%d media=%d", metadata.Load(), media.Load())
	}
}

// TestInstagramOldQueueSharesPostResolution 验证缓存为空时同帖并发资源共享一次解析。
func TestInstagramOldQueueSharesPostResolution(t *testing.T) {
	cli, address, metadata, media := instagramFixture(t, false, http.StatusOK)
	sub := domain.Subscription{Platform: "instagram", URL: address}
	var wg sync.WaitGroup
	// 模拟重启后的两条旧队列，没有扫描快照也只能产生一个帖子解析请求。
	for asset := 1; asset <= 2; asset++ {
		item := domain.Content{URL: address, RemoteID: fmt.Sprintf("fixture-%d", asset), Asset: asset}
		dir := t.TempDir()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := cli.Download(context.Background(), sub, item, domain.YouTubeOptions{}, dir, func(line string) { t.Log(line) }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// 并发执行后的请求计数比仅检查缓存命中日志更可靠。
	if metadata.Load() != 1 || media.Load() != 2 {
		t.Fatalf("metadata=%d media=%d", metadata.Load(), media.Load())
	}
}

// TestInstagramExpiredSourceRefreshesOnce 验证 403 只刷新一次，并继续使用原资源序号。
func TestInstagramExpiredSourceRefreshesOnce(t *testing.T) {
	cli, address, metadata, media := instagramFixture(t, true, http.StatusOK)
	sub := domain.Subscription{Platform: "instagram", URL: address}
	_, err := cli.Scan(context.Background(), sub, 10, func(line string) { t.Log(line) }, func(domain.ScanResult) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	item := domain.Content{URL: address, RemoteID: "fixture-2", Asset: 2}
	// 指定第二个资源，刷新后也不能误下载第一张照片。
	dir := t.TempDir()
	if err = cli.Download(context.Background(), sub, item, domain.YouTubeOptions{}, dir, func(line string) { t.Log(line) }); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, "fixture_2.gif")); err != nil {
		t.Fatal(err)
	}
	if metadata.Load() != 2 || media.Load() != 2 {
		t.Fatalf("metadata=%d media=%d", metadata.Load(), media.Load())
	}
}

// TestInstagram429DoesNotReparse 媒体限流不能触发额外帖子解析或无限刷新。
func TestInstagram429DoesNotReparse(t *testing.T) {
	cli, address, metadata, media := instagramFixture(t, false, http.StatusTooManyRequests)
	sub := domain.Subscription{Platform: "instagram", URL: address}
	item := domain.Content{URL: address, RemoteID: "fixture-1", Asset: 1}
	// 429 属于退避或失败，重新获取帖子地址不能解除这个状态。
	if err := cli.Download(context.Background(), sub, item, domain.YouTubeOptions{}, t.TempDir(), func(line string) { t.Log(line) }); err == nil {
		t.Fatal("429 应报告下载失败")
	}
	if metadata.Load() != 1 || media.Load() != 1 {
		t.Fatalf("metadata=%d media=%d", metadata.Load(), media.Load())
	}
}

// TestInstagramSourceCacheBoundAndExpiry 验证完整历史不会无限占用内存，过期地址也不复用。
func TestInstagramSourceCacheBoundAndExpiry(t *testing.T) {
	cache := newInstagramSourceCache()
	for i := 0; i < config.InstagramSourceCacheCapacity+1; i++ {
		// 超过容量一条即可检查边界，不需要创建或枚举真实历史。
		item := domain.Content{URL: fmt.Sprintf("post-%d", i), RemoteID: fmt.Sprint(i), Asset: 1}
		cache.put(domain.ScanResult{Items: []domain.Content{item}, MediaSources: map[string]domain.MediaSource{item.RemoteID: {URL: "media"}}})
	}
	if len(cache.entries) != config.InstagramSourceCacheCapacity {
		t.Fatal(len(cache.entries))
	}
	item := domain.Content{URL: "post-0", Asset: 1}
	if _, ok := cache.get(item); ok {
		t.Fatal("最早条目未被淘汰")
	}
	item.URL = "post-1"
	// 手动推进单条快照时间，验证过期行为无需等待十五分钟。
	key := instagramSourceKey(item.URL, item.Asset)
	entry := cache.entries[key]
	entry.at = time.Now().Add(-config.InstagramSourceCacheTTL - time.Second)
	cache.entries[key] = entry
	if _, ok := cache.get(item); ok {
		t.Fatal("过期地址仍被复用")
	}
}

// TestInstagramRateLimitPhase 验证已有引擎日志能够解释为何队列消费者未释放。
func TestInstagramRateLimitPhase(t *testing.T) {
	// 使用用户提供的真实限流日志格式，阶段说明必须保留具体等待原因。
	var phase, output string
	instagramPhaseLog(func(line string) { output += line }, func(value string) { phase = value })("[instagram][info] Waiting for 1 minutes until 23:48:34 (429 Too Many Requests)")
	if !strings.Contains(phase, "429") || !strings.Contains(output, "占用下载名额") {
		t.Fatal(phase, output)
	}
}

// TestInstagramResolutionWaiterCancellation 单个等待者取消不能清理共享解析。
func TestInstagramResolutionWaiterCancellation(t *testing.T) {
	cli := NewCLI("", nil)
	item := domain.Content{URL: "post", Asset: 1}
	flight := &instagramResolution{done: make(chan struct{})}
	cli.instagram.flights[item.URL] = flight
	ctx, cancel := context.WithCancel(context.Background())
	// 正在共享解析的任务只有等待通知，不应启动新的引擎进程。
	done := make(chan error, 1)
	go func() { _, err := cli.instagramSource(ctx, domain.Subscription{}, item, func(string) {}); done <- err }()
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	// 共享通知仍存在，原发起者和其他资源能够继续完成解析。
	if cli.instagram.flights[item.URL] != flight {
		t.Fatal("等待者取消清理了共享解析")
	}
}
