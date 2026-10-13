package service

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"strings"
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
	var lastReport time.Time
	started := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.mu.Lock()
		items, err := a.Repo.Contents()
		tasks, taskErr := a.Repo.Tasks()
		settings, settingsErr := a.Repo.Settings()
		subs, subsErr := a.Repo.Subscriptions()
		platforms := map[int64]string{}
		for _, sub := range subs {
			platforms[sub.ID] = sub.Platform
		}
		physicalActive := []string{}
		for _, task := range tasks {
			if task.ContentID != 0 && a.active[task.ID] != nil && platforms[task.SubscriptionID] == platforms[sid] {
				physicalActive = append(physicalActive, fmt.Sprintf("#%d(配置 #%d，%s)", task.ID, task.SubscriptionID, task.Status))
			}
		}
		writable := len(a.directories) > 1 && a.directories[1].Writable
		a.mu.Unlock()
		if err != nil {
			return err
		}
		if taskErr != nil {
			return taskErr
		}
		if settingsErr != nil {
			return settingsErr
		}
		if subsErr != nil {
			return subsErr
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
		// 背压与调度器使用相同执行范围；范围外积压没有消费者，不能占容量。
		queued, running := 0, 0
		active := []string{}
		for _, task := range selectedTasks(tasks, sid, settings.Debug.TestItemLimit) {
			if task.ContentID == 0 {
				continue
			}
			if task.Status == "queued" {
				queued++
			}
			if task.Status == "running" {
				running++
				stage := "等待引擎响应"
				for _, progress := range task.Progress {
					if progress.Status == "running" {
						stage = fmt.Sprintf("%s %.1f%% (%d/%d 字节)", progress.Kind, progress.Percent, progress.BytesDone, progress.BytesTotal)
						break
					}
				}
				active = append(active, fmt.Sprintf("#%d 内容 #%d · %s · 已运行 %s", task.ID, task.ContentID, stage, time.Since(task.StartedAt).Round(time.Second)))
			}
		}
		pending := queued + running
		allPending := 0
		for _, task := range tasks {
			if task.SubscriptionID == sid && task.ContentID != 0 && (task.Status == "queued" || task.Status == "running") {
				allPending++
			}
		}
		if pending < config.ScanQueueCapacity {
			if waiting {
				log(fmt.Sprintf("下载队列已释放容量，继续解析入队；等待 %s；当前容量 %d/%d", time.Since(started).Round(time.Second), pending, config.ScanQueueCapacity))
			}
			return nil
		}
		// 已入队记录继续保留；只有背压确实需要消费者时才报告目录不可写。
		if !writable {
			return fmt.Errorf("扫描无法继续：下载队列已满且下载目录不可写，消费者无法执行；请恢复目录后重试")
		}
		if !waiting || time.Since(lastReport) >= config.ScanLogInterval {
			limit := settings.YouTubeConcurrent
			if platforms[sid] == "instagram" {
				limit = settings.InstagramConcurrent
			}
			if len(active) == 0 {
				active = append(active, "无")
			}
			if len(physicalActive) == 0 {
				physicalActive = append(physicalActive, "无")
			}
			log(fmt.Sprintf("下载队列已满，暂停解析入队；等待入队资源 %s；等待 %s；容量 %d/%d；待执行 %d · 运行 %d；测试范围外积压 %d；调试限制 %d；平台并发上限 %d；下载任务：%s；平台活动消费者：%s", item.RemoteID, time.Since(started).Round(time.Second), pending, config.ScanQueueCapacity, queued, running, allPending-pending, settings.Debug.TestItemLimit, limit, strings.Join(active, "；"), strings.Join(physicalActive, "；")))
			lastReport = time.Now()
			waiting = true
		}
		// 等待不持有应用锁，显式唤醒调度器，使空闲名额及时认领持久队列。
		a.wakeScheduler()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
