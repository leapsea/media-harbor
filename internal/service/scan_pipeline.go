package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// streamScan 同步确认入队后才允许解析器读取下一条，避免额外内存预取。
// 生产者等待容量时下载消费者仍可独立运行，取消直接传递给解析器。
func (a *App) streamScan(ctx context.Context, sub domain.Subscription, limit int, kind string, log func(string), consume func(domain.ScanResult) error) (result domain.ScanResult, err error) {
	// 引擎异常仍保留已经交付的内容，并向任务返回具体错误。
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("扫描引擎异常：%v", value)
		}
	}()
	postAware := false
	if scanner, ok := a.Engine.(domain.PostScanner); ok {
		postAware = scanner.SupportsPostTasks()
	}
	var boundary atomic.Bool
	seenFull := map[string]bool{}
	if postAware {
		ctx = domain.WithPostGate(ctx, func(candidate domain.PostCandidate) (string, error) {
			current, readErr := a.Repo.Subscription(sub.ID)
			if readErr != nil {
				return "", readErr
			}
			settings, readErr := a.Repo.Settings()
			if readErr != nil {
				return "", readErr
			}
			current.RequestLanguage = settings.UI.Language
			options := settings.YouTubeOptionsFor(current)
			key := domain.PostIdentity(current.Platform, domain.Content{RemoteID: candidate.RemoteID, URL: candidate.URL})
			post, readErr := a.Repo.Post(sub.ID, key)
			if errors.Is(readErr, sql.ErrNoRows) {
				return "continue", nil
			}
			if readErr != nil {
				return "", readErr
			}
			if kind == "scan" && post.ManifestComplete && post.Status == "completed" && slices.Equal(post.Kinds, domain.ExpectedKinds(current.Platform, options)) {
				boundary.Store(true)
				log(fmt.Sprintf("增量扫描命中本地缓存；帖子 %s；帖子任务的全部 %d 个下载资源已完成，停止后续历史检查与分页", key, post.Expected))
				return "stop", nil
			}
			// 历史全量对已完成帖子返回 skip；初始化现在必须重新解析所有发现条目。
			return "continue", nil
		})
	}
	known := map[string]bool{}
	indexed := map[string]domain.Content{}
	var knownIDs []string
	{
		items, readErr := a.Repo.Contents()
		if readErr != nil {
			return result, readErr
		}
		for _, item := range items {
			if item.SubscriptionID == sub.ID {
				indexed[item.RemoteID] = item
				if kind == "scan" && !postAware {
					known[item.RemoteID] = true
					knownIDs = append(knownIDs, item.RemoteID)
				}
			}
		}
	}
	if postAware && kind == "scan" {
		posts, readErr := a.Repo.Posts(sub.ID)
		if readErr != nil {
			return result, readErr
		}
		for _, post := range posts {
			if post.ManifestComplete && post.Status == "completed" {
				knownIDs = append(knownIDs, post.RemoteID)
			}
		}
	}
	emitted := 0
	deliver := func(batch domain.ScanResult) error {
		if len(batch.Items) == 0 {
			return consume(batch)
		}
		for _, item := range batch.Items {
			// 全量同一轮也只解析每个媒体一次，平台重复交付不放大请求或下载。
			if kind == "full" {
				if seenFull[item.RemoteID] {
					continue
				}
				seenFull[item.RemoteID] = true
			}
			// 每个帖子任务的决策读取当前订阅配置和设置，GUI/CLI 修改参与下一条处理。
			current, readErr := a.Repo.Subscription(sub.ID)
			if readErr != nil {
				return readErr
			}
			settings, readErr := a.Repo.Settings()
			if readErr != nil {
				return readErr
			}
			current.RequestLanguage = settings.UI.Language
			options := settings.YouTubeOptionsFor(current)
			if postAware {
				action, gateErr := domain.GatePost(ctx, domain.PostCandidate{RemoteID: item.RemoteID, URL: item.URL})
				if gateErr != nil {
					return gateErr
				}
				if action == "stop" {
					return domain.ErrScanBoundary
				}
				if action == "skip" {
					continue
				}
			}
			post, registerErr := a.registerPost(ctx, sub.ID, current.Platform, item, options)
			if registerErr != nil {
				return registerErr
			}
			item.PostID = post.ID
			item.Overwrite = kind == "full"

			// 先检查增量边界，已有内容不再请求完整视频信息或媒体接口。
			if known[item.RemoteID] {
				log(fmt.Sprintf("增量扫描命中本地缓存；媒体 %s；停止后续历史检查与分页", item.RemoteID))
				return domain.ErrScanBoundary
			}
			if limit > 0 && emitted >= limit {
				break
			}
			if err := a.waitScanQueue(ctx, sub.ID, item, kind == "catchup", log); err != nil {
				return err
			}
			if preparer, ok := a.Engine.(domain.MediaPreparer); ok {
				// 已完成且无需补全、已排队或正在下载的条目不额外准备格式清单。
				existing, exists := indexed[item.RemoteID]
				needed := kind == "full" || !exists || (existing.Status != "queued" && existing.Status != "running" &&
					(existing.Status != "completed" || (kind == "catchup" && engine.NeedsSupplement(sub.Platform, existing, options))))
				if postAware && exists && existing.Status == "completed" {
					required, readErr := a.contentNeedsDownload(existing)
					if readErr != nil {
						return readErr
					}
					needed = needed || required
				}
				if needed {
					prepared, err := preparer.Prepare(ctx, current, item, options, log)
					if err != nil {
						return err
					}
					item = prepared
				}
			}
			// 这里只交付一个媒体，缓存描述由引擎持有，不写进公开内容索引。
			if err := consume(domain.ScanResult{StableID: batch.StableID, CurrentName: batch.CurrentName, Items: []domain.Content{item}}); err != nil {
				return err
			}
			emitted++
			if _, exists := indexed[item.RemoteID]; !exists {
				indexed[item.RemoteID] = domain.Content{Status: "queued"}
			}
			// 达到容量后不发出 SDK 确认，下一条获取与解析都要等消费者取走任务。
			if limit == 0 || emitted < limit {
				if err := a.waitScanQueue(ctx, sub.ID, domain.Content{RemoteID: "下一条"}, false, log); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// 恢复任务可能已经占满等待队列，首次平台请求前也遵循相同背压。
	if err = a.waitScanQueue(ctx, sub.ID, domain.Content{RemoteID: "下一条"}, false, log); err != nil {
		return result, err
	}
	if scanner, ok := a.Engine.(domain.IncrementalScanner); ok && kind == "scan" {
		result, err = scanner.ScanIncremental(ctx, sub, limit, knownIDs, log, deliver)
	} else {
		result, err = a.Engine.Scan(ctx, sub, limit, log, deliver)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(err, domain.ErrScanBoundary) || boundary.Load() {
		err = nil
	}
	return result, err
}

// waitScanQueue 限制持久队列提前入队数量，已完成或已排队的重复项不等待容量。
// 消费速度不足时停止逐条确认，将背压传递给平台提取器。
func (a *App) waitScanQueue(ctx context.Context, sid int64, item domain.Content, catchup bool, log func(string)) error {
	ticker := time.NewTicker(config.ScanQueueCheckInterval)
	defer ticker.Stop()
	waiting := false
	var lastReport time.Time
	started := time.Now()
	for {
		sub, err := a.Repo.Subscription(sid)
		if err != nil {
			return err
		}
		if err := a.Credentials.Check(sub.Platform); err != nil {
			return err
		}
		// 历史资产：这里只检查 ctx.Err()，容量释放后直接继续生产。
		if err := a.waitProductionAwake(ctx, sub.Platform, item.RemoteID, log); err != nil {
			return err
		}
		a.mu.Lock()
		// 下载队列和设置取同一调度视图，容量判断不持锁等待。
		items, err := a.Repo.Contents()
		tasks, taskErr := a.Repo.Tasks()
		settings, settingsErr := a.Repo.Settings()
		subs, subsErr := a.Repo.Subscriptions()
		platforms := map[int64]string{}
		for _, sub := range subs {
			platforms[sub.ID] = sub.Platform
		}
		physicalActive := []string{}
		// 取消中的引擎可能仍在退出，诊断区分持久状态和实际进程。
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
				// 已被消费者持有的重复项不新增任务，不占额外容量。
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
				options := settings.YouTubeOptionsFor(sub)
				if !engine.NeedsSupplement(sub.Platform, known, options) {
					// 只有补全读取已有产物；增量早停在上层生产回调完成。
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
						if progress.Phase != "" {
							stage = progress.Phase + " · " + stage
						}
						break
					}
				}
				active = append(active, fmt.Sprintf("#%d 内容 #%d · %s · 已运行 %s", task.ID, task.ContentID, stage, time.Since(task.StartedAt).Round(time.Second)))
			}
		}
		pending := queued
		// 正在运行的消费者使用下载名额，不占等待队列容量。
		capacity := settings.Pipeline.QueueCapacity
		// 全部积压只用于日志解释，调试范围外任务不能形成背压。
		allPending := 0
		for _, task := range tasks {
			if task.SubscriptionID == sid && task.ContentID != 0 && (task.Status == "queued" || task.Status == "running") {
				allPending++
			}
		}
		if pending < capacity {
			// 容量可用时立即返回，让当前媒体持久入队。
			if waiting {
				log(fmt.Sprintf("下载队列已释放容量，继续解析入队；等待 %s；当前容量 %d/%d", time.Since(started).Round(time.Second), pending, capacity))
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
			log(fmt.Sprintf("下载队列已满，暂停解析入队；等待入队资源 %s；等待 %s；容量 %d/%d；待执行 %d · 运行 %d；测试范围外积压 %d；调试限制 %d；平台并发上限 %d；下载任务：%s；平台活动消费者：%s", item.RemoteID, time.Since(started).Round(time.Second), pending, capacity, queued, running, allPending-queued-running, settings.Debug.TestItemLimit, limit, strings.Join(active, "；"), strings.Join(physicalActive, "；")))
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

// 历史资产：旧有界通道在持久队列满后仍允许提前解析，现由逐条确认替代。
// // streamScan 用有界通道隔离解析与持久入队，满时 emit 阻塞而不是继续积压。
// // 每次只传一个媒体；取消会同时唤醒通道写入及下载队列等待。
// func (a *App) streamScan(ctx context.Context, sub domain.Subscription, limit int, kind string, log func(string), consume func(domain.ScanResult) error) (domain.ScanResult, error) {
// 	parent := ctx
// 	// 子上下文仅控制本次枚举，正常缓存边界不会取消随后继续下载的新项。
// 	ctx, cancel := context.WithCancel(ctx)
// 	defer cancel()
// 	// 固定扫描开始前的索引，新入队条目及轮播重复不能成为本轮停止边界。
// 	known := map[string]bool{}
// 	var knownIDs []string
// 	if kind == "scan" {
// 		// 只读一次本地索引；全量和补全没有早停快照。
// 		items, err := a.Repo.Contents()
// 		if err != nil {
// 			return domain.ScanResult{}, err
// 		}
// 		for _, item := range items {
// 			// 其他博主的索引不能截断当前博主的扫描。
// 			if item.SubscriptionID == sub.ID {
// 				known[item.RemoteID] = true
// 				knownIDs = append(knownIDs, item.RemoteID)
// 			}
// 		}
// 	}
// 	queue := make(chan domain.ScanResult, config.ScanQueueCapacity)
// 	// 引擎结束和入队结束独立等待，先排空已发现的新内容。
// 	type outcome struct {
// 		result domain.ScanResult
// 		err    error
// 	}
// 	done := make(chan outcome, 1)
// 	go func() {
// 		var result domain.ScanResult
// 		var err error
// 		defer func() {
// 			// 引擎异常仍解除消费者等待，不遗留关闭不了的扫描通道。
// 			if value := recover(); value != nil {
// 				err = fmt.Errorf("扫描引擎异常：%v", value)
// 			}
// 			close(queue)
// 			done <- outcome{result, err}
// 		}()
// 		emitted := 0
// 		deliver := func(batch domain.ScanResult) error {
// 			send := func(v domain.ScanResult) error {
// 				select {
// 				case queue <- v:
// 					return nil
// 				case <-ctx.Done():
// 					return ctx.Err()
// 				}
// 			}
// 			if len(batch.Items) == 0 {
// 				// 作者身份等非媒体消息仍需交付，但不会触发增量边界。
// 				return send(batch)
// 			}
// 			for _, item := range batch.Items {
// 				// 在生产者回调处停止，不能等缓冲通道消费后才取消历史请求。
// 				if known[item.RemoteID] {
// 					log(fmt.Sprintf("增量扫描命中本地缓存；媒体 %s；停止后续历史检查与分页", item.RemoteID))
// 					return domain.ErrScanBoundary
// 				}
// 				if limit > 0 && emitted >= limit {
// 					// 调试条目限制同时约束入队数量，保持各模式范围一致。
// 					break
// 				}
// 				if err := send(domain.ScanResult{StableID: batch.StableID, CurrentName: batch.CurrentName, Items: []domain.Content{item}}); err != nil {
// 					return err
// 				}
// 				emitted++
// 			}
// 			return nil
// 		}
// 		if scanner, ok := a.Engine.(domain.IncrementalScanner); ok && kind == "scan" {
// 			// 平台可在输出媒体前匹配缓存，避免预读更旧的列表页。
// 			result, err = scanner.ScanIncremental(ctx, sub, limit, knownIDs, log, deliver)
// 		} else {
// 			// 历史路径所有扫描直接调用 Engine.Scan，现保留给全量、补全及兼容引擎。
// 			result, err = a.Engine.Scan(ctx, sub, limit, log, deliver)
// 		}
// 	}()
// 	var consumeErr error
// 	for batch := range queue {
// 		if consumeErr != nil {
// 			// 排空通道以便生产者退出，保留首次入队失败作为最终原因。
// 			continue
// 		}
// 		if len(batch.Items) > 0 {
// 			consumeErr = a.waitScanQueue(ctx, sub.ID, batch.Items[0], kind == "catchup", log)
// 		}
// 		if consumeErr == nil {
// 			consumeErr = consume(batch)
// 		}
// 		if consumeErr != nil {
// 			// 持久化失败时也立即终止平台解析，不能继续丢弃历史响应。
// 			cancel()
// 		}
// 	}
// 	result := <-done
// 	if consumeErr != nil {
// 		return result.result, consumeErr
// 	}
// 	if parent.Err() != nil {
// 		return result.result, parent.Err()
// 	}
// 	// 只归一化主动到达缓存边界；用户取消、平台和入队错误仍按失败返回。
// 	if errors.Is(result.err, domain.ErrScanBoundary) {
// 		return result.result, nil
// 	}
// 	return result.result, result.err
// }
//
