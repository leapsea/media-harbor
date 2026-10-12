package service

import (
	"context"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"time"
)

// streamScan 用有界通道隔离解析与持久入队，满时 emit 阻塞而不是继续积压。
// 每次只传一个媒体；取消会同时唤醒通道写入及下载队列等待。
func (a *App) streamScan(ctx context.Context, sub domain.Subscription, limit int, catchup bool, log func(string), consume func(domain.ScanResult) error) (domain.ScanResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan domain.ScanResult, config.ScanQueueCapacity)
	type outcome struct {
		result domain.ScanResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		emitted := 0
		result, err := a.Engine.Scan(ctx, sub, limit, log, func(batch domain.ScanResult) error {
			send := func(v domain.ScanResult) error {
				select {
				case queue <- v:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if len(batch.Items) == 0 {
				return send(batch)
			}
			for _, item := range batch.Items {
				if limit > 0 && emitted >= limit {
					break
				}
				if err := send(domain.ScanResult{StableID: batch.StableID, CurrentName: batch.CurrentName, Items: []domain.Content{item}}); err != nil {
					return err
				}
				emitted++
			}
			return nil
		})
		close(queue)
		done <- outcome{result, err}
	}()
	var consumeErr error
	for batch := range queue {
		if consumeErr != nil {
			continue
		}
		if len(batch.Items) > 0 {
			consumeErr = a.waitScanQueue(ctx, sub.ID, batch.Items[0], catchup, log)
		}
		if consumeErr == nil {
			consumeErr = consume(batch)
		}
		if consumeErr != nil {
			cancel()
		}
	}
	result := <-done
	if consumeErr != nil {
		return result.result, consumeErr
	}
	return result.result, result.err
}

// waitScanQueue 限制持久队列提前入队数量，已完成或已排队的重复项不等待容量。
// 消费速度不足时停止取下一条通道数据，将背压传递给引擎输出读取。
func (a *App) waitScanQueue(ctx context.Context, sid int64, item domain.Content, catchup bool, log func(string)) error {
	ticker := time.NewTicker(config.ScanQueueCheckInterval)
	defer ticker.Stop()
	waiting := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.mu.Lock()
		items, err := a.Repo.Contents()
		tasks, taskErr := a.Repo.Tasks()
		a.mu.Unlock()
		if err != nil {
			return err
		}
		if taskErr != nil {
			return taskErr
		}
		for _, known := range items {
			if known.SubscriptionID != sid || known.RemoteID != item.RemoteID {
				continue
			}
			if known.Status == "queued" || known.Status == "running" {
				return nil
			}
			if known.Status == "completed" {
				if !catchup {
					return nil
				}
				sub, readErr := a.Repo.Subscription(sid)
				if readErr != nil {
					return readErr
				}
				settings, readErr := a.Repo.Settings()
				if readErr != nil {
					return readErr
				}
				options := settings.YouTube
				if sub.YouTube != nil {
					options = *sub.YouTube
				}
				if !engine.NeedsSupplement(sub.Platform, known, options) {
					return nil
				}
			}
		}
		pending := 0
		for _, t := range tasks {
			if t.SubscriptionID == sid && t.ContentID != 0 && (t.Status == "queued" || t.Status == "running") {
				pending++
			}
		}
		if pending < config.ScanQueueCapacity {
			if waiting {
				log("下载队列已释放容量，继续解析入队")
			}
			return nil
		}
		if !waiting {
			log("下载队列已满，暂停解析入队，等待消费者处理")
			waiting = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
