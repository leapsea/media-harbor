package service

import (
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"time"
)

// retryContentLocked 复用原下载任务及中间文件，调用方持有应用锁。
// 恢复需要补全或刷新元数据的条目，等待与运行中的任务不重复排队。
func (a *App) retryContentLocked(existing domain.Content, discovered domain.Content, task domain.Task) error {
	existing.URL = discovered.URL
	existing.Title = discovered.Title
	// 扁平扫描可能没有日期，不能清空上次下载解析的发布时间。
	if !discovered.Published.IsZero() {
		existing.Published = discovered.Published
	}
	existing.Asset = discovered.Asset
	if discovered.ChannelID != "" {
		existing.ChannelID = discovered.ChannelID
	}
	if discovered.Availability != "" {
		existing.Availability = discovered.Availability
	}
	existing.Status = "queued"
	existing.Error = ""
	if err := a.Repo.SaveContent(&existing); err != nil {
		return err
	}
	if task.ID == 0 {
		// 内容已发现但没有下载任务时，为原内容身份补建任务。
		_, err := a.Repo.Enqueue(existing.SubscriptionID, existing.ID, "download")
		return err
	}
	// 已有失败任务重新排队，保留尝试次数，成功后仍使用原去重 ID。
	task.Status = "queued"
	task.Error = ""
	task.FinishedAt = time.Time{}
	return a.Repo.SaveTask(&task)
}

// reconcileScanLocked 对照本地下载结果处理枚举内容；扫描不会删除平台未返回的记录。
// 普通扫描维持只发现新增内容；补全按产物缺项及元数据刷新需求重新排队。
func (a *App) reconcileScanLocked(subscriptionID int64, items []domain.Content, catchup bool) (added, retried, completed, pending, skipped int, err error) {
	contents, err := a.Repo.Contents()
	if err != nil {
		return
	}
	known := map[string]domain.Content{}
	for _, item := range contents {
		if item.SubscriptionID == subscriptionID {
			known[item.RemoteID] = item
		}
	}
	tasks, err := a.Repo.Tasks()
	if err != nil {
		return
	}
	byContent := map[int64]domain.Task{}
	for _, task := range tasks {
		if task.SubscriptionID == subscriptionID && task.ContentID != 0 && task.ID > byContent[task.ContentID].ID {
			byContent[task.ContentID] = task
		}
	}
	var sub domain.Subscription
	var settings domain.Settings
	if catchup {
		sub, err = a.Repo.Subscription(subscriptionID)
		if err != nil {
			return
		}
		settings, err = a.Repo.Settings()
		if err != nil {
			return
		}
	}
	for _, item := range items {
		existing, exists := known[item.RemoteID]
		if !exists {
			if err = a.Repo.AddContentAndTask(subscriptionID, item); err != nil {
				return
			}
			// 同批次重复媒体按待执行处理，不再次创建内容或扫描任务。
			item.Status = "queued"
			known[item.RemoteID] = item
			added++
			continue
		}
		if !catchup {
			skipped++
			continue
		}
		options := settings.YouTube
		if sub.YouTube != nil {
			options = *sub.YouTube
		}
		// 补全不是权限重置；首次失败及跳过的付费条目保持原状态。
		access := existing
		if item.Availability != "" {
			access.Availability = item.Availability
		}
		if item.ChannelID != "" {
			access.ChannelID = item.ChannelID
		}
		if sub.Platform == "youtube" && domain.IsPaidAvailability(access.Availability) {
			blocked, readErr := a.Repo.PaidFailure(paidChannel(access, sub))
			if readErr != nil {
				err = readErr
				return
			}
			if blocked > 0 {
				skipped++
				continue
			}
		}
		if existing.Status == "completed" && !engine.NeedsSupplement(sub.Platform, existing, options) {
			completed++
			continue
		}
		task := byContent[existing.ID]
		if existing.Status == "queued" || existing.Status == "running" || task.Status == "queued" || task.Status == "running" {
			pending++
			continue
		}
		if err = a.retryContentLocked(existing, item, task); err != nil {
			return
		}
		existing.Status = "queued"
		known[item.RemoteID] = existing
		retried++
	}
	return
}
