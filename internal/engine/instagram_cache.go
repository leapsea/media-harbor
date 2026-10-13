package engine

import (
	"context"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"strconv"
	"sync"
	"time"
)

type instagramSourceEntry struct {
	source domain.MediaSource
	at     time.Time
}

// done 发布解析结束，关闭通道后等待者才能读取 err。
type instagramResolution struct {
	done chan struct{}
	err  error
}

// instagramSourceCache 保留有界的会话快照，进程重启或淘汰后仍能从原帖子解析。
// 同帖解析使用完成通知共享结果，等待者取消不影响其他下载。
type instagramSourceCache struct {
	mu      sync.Mutex
	entries map[string]instagramSourceEntry
	flights map[string]*instagramResolution
}

func newInstagramSourceCache() *instagramSourceCache {
	return &instagramSourceCache{entries: map[string]instagramSourceEntry{}, flights: map[string]*instagramResolution{}}
}

// URL 加媒体序号区分轮播资源，不把某张照片的地址用于同帖其他媒体。
func instagramSourceKey(address string, asset int) string {
	return address + "\x00" + strconv.Itoa(asset)
}

// put 在扫描交付时缓存媒体描述；元数据之后只读，消费者不会修改共享快照。
func (cache *instagramSourceCache) put(batch domain.ScanResult) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for _, item := range batch.Items {
		// 缺少描述的旧结果仍允许入队，下载时会使用原帖子进行解析。
		source, ok := batch.MediaSources[item.RemoteID]
		if !ok || source.URL == "" {
			continue
		}
		cache.entries[instagramSourceKey(item.URL, item.Asset)] = instagramSourceEntry{source, time.Now()}
		for len(cache.entries) > config.InstagramSourceCacheCapacity {
			// 淘汰最早快照，队列再大也不会无限占用引擎内存。
			var oldest string
			var at time.Time
			for key, entry := range cache.entries {
				if at.IsZero() || entry.at.Before(at) {
					oldest, at = key, entry.at
				}
			}
			delete(cache.entries, oldest)
		}
	}
}

func (cache *instagramSourceCache) get(item domain.Content) (domain.MediaSource, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	key := instagramSourceKey(item.URL, item.Asset)
	entry, ok := cache.entries[key]
	// 时间窗口仅控制快照复用，不改变内容去重 ID 或历史记录。
	if !ok || time.Since(entry.at) > config.InstagramSourceCacheTTL {
		delete(cache.entries, key)
		return domain.MediaSource{}, false
	}
	return entry.source, true
}

// invalidate 只丢弃仍指向被拒绝地址的快照，不撤销其他消费者刚刷新的结果。
func (cache *instagramSourceCache) invalidate(item domain.Content, rejected string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries[instagramSourceKey(item.URL, item.Asset)].source.URL != rejected {
		return
	}
	// 同一帖子的签名地址来自一次解析，失效时将该次轮播快照一起更新。
	for key := range cache.entries {
		if len(key) > len(item.URL) && key[:len(item.URL)+1] == item.URL+"\x00" {
			delete(cache.entries, key)
		}
	}
}

// instagramSource 针对重启后或旧队列解析整个帖子一次，轮播下载共享这次结果。
func (c CLI) instagramSource(ctx context.Context, sub domain.Subscription, item domain.Content, log func(string)) (domain.MediaSource, error) {
	if err := ctx.Err(); err != nil {
		return domain.MediaSource{}, err
	}
	cache := c.instagram
	if source, ok := cache.get(item); ok {
		log(fmt.Sprintf("复用 Instagram 已解析媒体地址；资源 %s；跳过重复帖子接口请求", item.RemoteID))
		return source, nil
	}
	cache.mu.Lock()
	// 首次检查与认领之间可能已有解析完成，重新检查避免紧接着重复请求。
	if entry, ok := cache.entries[instagramSourceKey(item.URL, item.Asset)]; ok && time.Since(entry.at) <= config.InstagramSourceCacheTTL {
		cache.mu.Unlock()
		log("复用刚完成的同帖媒体解析结果")
		return entry.source, nil
	}
	flight, waiting := cache.flights[item.URL]
	// 仅同帖复用解析，不占用额外下载名额，也不串行化不同帖子。
	if !waiting {
		flight = &instagramResolution{done: make(chan struct{})}
		cache.flights[item.URL] = flight
	}
	cache.mu.Unlock()
	if waiting {
		log("同一帖子正在解析，共享本次结果；等待期间不另发帖子接口请求")
		select {
		case <-ctx.Done():
			return domain.MediaSource{}, ctx.Err()
		case <-flight.done:
		}
		// 发起者单独取消时，仍在运行的资源可以接管解析；整组取消不会继续请求。
		if errors.Is(flight.err, context.Canceled) && ctx.Err() == nil {
			return c.instagramSource(ctx, sub, item, log)
		}
		if flight.err != nil {
			return domain.MediaSource{}, flight.err
		}
	} else {
		started := time.Now()
		log(fmt.Sprintf("解析 Instagram 帖子媒体地址；资源 %s；一次解析供同帖资源复用", item.RemoteID))
		sub.URL = item.URL
		// 只枚举当前帖子，整个频道的扫描与队列生命周期仍由服务层管理。
		_, err := c.Scan(ctx, sub, 0, log, func(domain.ScanResult) error { return nil })
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		cache.mu.Lock()
		flight.err = err
		// 结果入缓存后再发布结束通知，所有等待者都可取得自己的资源序号。
		delete(cache.flights, item.URL)
		close(flight.done)
		cache.mu.Unlock()
		log(fmt.Sprintf("Instagram 帖子解析结束；资源 %s；耗时 %s；成功 %t", item.RemoteID, time.Since(started).Round(time.Millisecond), err == nil))
		if err != nil {
			return domain.MediaSource{}, err
		}
	}
	if source, ok := cache.get(item); ok {
		return source, nil
	}
	// 缺少目标资源应返回具体错误，不能把其他媒体替代为本任务的结果。
	return domain.MediaSource{}, fmt.Errorf("Instagram 帖子未返回资源序号 %d", item.Asset)
}
