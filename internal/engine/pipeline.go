package engine

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"sync"
	"time"
)

// platformPipeline 把解析执行权与下载名额分开；两个平台各自拥有实例。
// 设置更新唤醒等待者，降低并发不取消已经传输的文件。
type platformPipeline struct {
	producer chan struct{}
	// 消费计数与通知在同一锁内更新，等待者不会漏掉释放或设置更新。
	mu               sync.Mutex
	consumers, limit int
	changed          chan struct{}
	requests         *instagramRequests
	metadata         map[string]preparedYouTube
}

// preparedYouTube 只在引擎内存保存临时签名地址，解析失败交给对应下载任务报告。
type preparedYouTube struct {
	raw []byte
	err error
	at  time.Time
}

func newPlatformPipeline(requests *instagramRequests, limit int) *platformPipeline {
	return &platformPipeline{producer: make(chan struct{}, 1), limit: limit, changed: make(chan struct{}), requests: requests, metadata: map[string]preparedYouTube{}}
}

// acquireProducer 的许可覆盖整个获取和解析操作；交付回调期间主动归还。
func (p *platformPipeline) acquireProducer(ctx context.Context, log func(string)) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// 空闲时直接执行；争用时留下阶段日志，等待本身不持有应用锁。
	started := time.Now()
	select {
	case p.producer <- struct{}{}:
	default:
		log("生产者忙，等待串行解析执行权")
		select {
		case p.producer <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if time.Since(started) > time.Second {
		log(fmt.Sprintf("生产者恢复；等待 %s", time.Since(started).Round(time.Millisecond)))
	}
	return sync.OnceFunc(func() { <-p.producer }), nil
}

// acquireConsumer 也约束头像传输，避免扫描旁路下载突破媒体并发数。
func (p *platformPipeline) acquireConsumer(ctx context.Context, log func(string)) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		// 名额在启动进程前认领，失败和取消都通过幂等回调归还。
		if p.consumers < p.limit {
			p.consumers++
			p.mu.Unlock()
			return sync.OnceFunc(func() { p.mu.Lock(); p.consumers--; close(p.changed); p.changed = make(chan struct{}); p.mu.Unlock() }), nil
		}
		changed := p.changed
		p.mu.Unlock()
		log("等待下载消费者名额")
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (c CLI) pipeline(platform string) *platformPipeline {
	// 字面量 CLI 留给独立工具与旧协议替身测试。
	if c.pipelines == nil {
		return nil
	}
	return c.pipelines[platform]
}

// Configure 只更新调度参数，已经持有的时钟、429期限和执行归属继续保留。
func (c CLI) Configure(s domain.Settings) {
	// 参数共享，各平台保存自己的时钟和消费者数。
	for platform, p := range c.pipelines {
		options, limit := s.Pipeline.YouTube, s.YouTubeConcurrent
		if platform == "instagram" {
			options, limit = s.Pipeline.Instagram, s.InstagramConcurrent
		}
		p.mu.Lock()
		// 缩小并发时等待现有下载自行结束。
		p.limit = max(1, limit)
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
		p.requests.configure(options)
	}
	// 有效期按当前值判断，降低容量立即淘汰旧描述。
	if c.instagram != nil {
		c.instagram.mu.Lock()
		c.instagram.ttl = time.Duration(s.Pipeline.InstagramSourceCacheTTLMinutes) * time.Minute
		c.instagram.capacity = s.Pipeline.InstagramSourceCacheCapacity
		c.instagram.trim()
		c.instagram.mu.Unlock()
	}
}

// consumer 在独立工具中沿用原行为；服务实例统一使用平台名额。
func (c CLI) consumer(ctx context.Context, platform string, log func(string)) (func(), error) {
	if p := c.pipeline(platform); p != nil {
		return p.acquireConsumer(ctx, log)
	}
	return func() {}, nil
}

// Prepare 在队列有容量且增量边界检查通过后执行，不再次检查本地历史。
func (c CLI) Prepare(ctx context.Context, sub domain.Subscription, item domain.Content, options domain.YouTubeOptions, log func(string)) error {
	if sub.Platform != "youtube" || c.pipeline(sub.Platform) == nil {
		return nil
	}
	_, err := c.youtubeMetadata(ctx, sub, item, options, log, false)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		log(fmt.Sprintf("生产者解析失败；媒体 %s；下载任务将记录错误：%v", item.RemoteID, err))
	}
	return nil
}

// 保存完整视频信息供消费者复用；重试明确要求 fresh 时才重新获取地址。
func (c CLI) youtubeMetadata(ctx context.Context, sub domain.Subscription, item domain.Content, options domain.YouTubeOptions, log func(string), fresh bool) ([]byte, error) {
	p := c.pipeline("youtube")
	// 语言与附件选项参与缓存身份，避免复用另一套格式选择。
	key := youtubeMetadataKey(sub, item, options)
	if p != nil {
		p.mu.Lock()
		entry, ok := p.metadata[key]
		p.mu.Unlock()
		if !fresh && ok && time.Since(entry.at) < config.YouTubeMetadataCacheTTL {
			log("消费者复用生产者已解析的 YouTube 信息；媒体 " + item.RemoteID)
			return entry.raw, entry.err
		}
		release, err := p.acquireProducer(ctx, log)
		if err != nil {
			return nil, err
		}
		defer release()
		// 同一个视频可能由入队准备和恢复任务同时请求，获得执行权后再次核对。
		p.mu.Lock()
		entry, ok = p.metadata[key]
		p.mu.Unlock()
		if !fresh && ok && time.Since(entry.at) < config.YouTubeMetadataCacheTTL {
			return entry.raw, entry.err
		}
	}
	log("生产者：解析 YouTube 格式与发布日期；媒体 " + item.RemoteID)
	args := append(c.youtubeMetadataArgs(sub, options), "--", item.URL)
	raw, err := c.runYouTube(ctx, args, log, true, "metadata")
	// 主动取消不发布失败快照，后续任务仍能接管同一视频。
	if p != nil && ctx.Err() == nil {
		p.mu.Lock()
		p.metadata[key] = preparedYouTube{raw, err, time.Now()}
		// 队列有界之外再限制工具调用和失败记录，避免会话长期累积。
		for len(p.metadata) > config.YouTubeMetadataCacheCapacity {
			oldest := key
			for k, v := range p.metadata {
				if v.at.Before(p.metadata[oldest].at) {
					oldest = k
				}
			}
			delete(p.metadata, oldest)
		}
		p.mu.Unlock()
	}
	return raw, err
}

func youtubeMetadataKey(sub domain.Subscription, item domain.Content, options domain.YouTubeOptions) string {
	return sub.URL + "\x00" + item.URL + "\x00" + fmt.Sprint(options) + "\x00" + sub.RequestLanguage
}

// 失败只交付当前消费者一次；手动重试重新排入生产者，不能长期复用失败。
func (c CLI) forgetYouTubeError(sub domain.Subscription, item domain.Content, options domain.YouTubeOptions) {
	if p := c.pipeline("youtube"); p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		key := youtubeMetadataKey(sub, item, options)
		if p.metadata[key].err != nil {
			delete(p.metadata, key)
		}
	}
}
