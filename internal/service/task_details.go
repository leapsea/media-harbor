package service

import (
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"sort"
	"time"
)

// DetailPart 对应持久 C 子任务；操作 ID 与执行时间仍来自执行账本。
type DetailPart struct {
	domain.TransferProgress
	ResourceID int64    `json:"resource_id"`
	TaskID     int64    `json:"task_id"`
	Labels     []string `json:"labels"`
	Number     int      `json:"number,omitempty"`
}

// DetailContent 直接投影 B；期望清单与完整状态不再由可见子项推算。
type DetailContent struct {
	Key              string       `json:"key"`
	PostID           int64        `json:"post_id"`
	Title            string       `json:"title"`
	Status           string       `json:"status"`
	ManifestComplete bool         `json:"manifest_complete"`
	Expected         int          `json:"expected"`
	Completed        int          `json:"completed"`
	Parts            []DetailPart `json:"parts"`
	Error            string       `json:"error,omitempty"`
}
type TaskDetails struct {
	Platform string          `json:"platform"`
	Items    []DetailContent `json:"items"`
}

// TaskDetails 从 B/C 关联读取领域状态。旧任务/内容只补字节、文件身份与执行排序。
func (a *App) TaskDetails(sid int64) (TaskDetails, error) {
	sub, err := a.Repo.Subscription(sid)
	if err != nil {
		return TaskDetails{}, err
	}
	settings, err := a.Repo.Settings()
	if err != nil {
		return TaskDetails{}, err
	}
	rows, err := a.Repo.PostDownloadRows(sid, settings.Debug.TestItemLimit)
	if err != nil {
		return TaskDetails{}, err
	}
	result := TaskDetails{Platform: sub.Platform, Items: []DetailContent{}}
	index := map[int64]int{}
	numbers := map[string]int{}
	created := map[int64]time.Time{}
	started := map[int64]time.Time{}
	labels := map[string]string{"video": "视频", "metadata": "元数据", "comments": "评论", "subtitles": "字幕", "thumbnail": "缩略图", "audio": "音频", "extracted": "本地提取音频"}
	for _, row := range rows {
		post, resource, task := row.Post, row.Download, row.Task
		i, exists := index[post.ID]
		if !exists {
			i = len(result.Items)
			index[post.ID] = i
			result.Items = append(result.Items, DetailContent{Key: post.RemoteID, PostID: post.ID, Title: post.Title, Status: post.Status, ManifestComplete: post.ManifestComplete, Expected: post.Expected, Completed: post.Completed, Parts: []DetailPart{}, Error: post.Error})
			if post.Title == "" {
				result.Items[i].Title = post.RemoteID
			}
			created[post.ID] = task.CreatedAt
		}
		if !task.StartedAt.IsZero() && (started[post.ID].IsZero() || task.StartedAt.Before(started[post.ID])) {
			started[post.ID] = task.StartedAt
		}
		p := resource.Progress
		p.Kind = resource.Kind
		p.Status = resource.Status
		p.Error = resource.Error
		if p.Status == "completed" && resource.Outcome == "unavailable" {
			p.Status = "skipped"
		}
		// 升级前资源缺少字节事件时复用已有产物读取，不改变 C 的持久完成判定。
		if p.BytesTotal == 0 && resource.Status == "completed" && resource.Outcome != "unavailable" {
			kind := resource.Kind
			if kind == "comments" {
				kind = "metadata"
			}
			artifact := engine.ArtifactProgress(kind, domain.Content{Paths: resource.Paths})
			p.BytesTotal, p.BytesDone, p.Percent = artifact.BytesTotal, artifact.BytesDone, artifact.Percent
		}
		if p.Title != "" {
			result.Items[i].Title = p.Title
		}
		label, number := labels[resource.Kind], 0
		if resource.Kind == "media" {
			label = "照片"
			if row.Content.MediaType == "video" || engine.Inventory(row.Content, "").Video() != "" {
				label = "视频"
			}
			key := post.RemoteID + label
			numbers[key]++
			number = numbers[key]
		}
		result.Items[i].Parts = append(result.Items[i].Parts, DetailPart{TransferProgress: p, ResourceID: resource.ID, TaskID: task.ID, Labels: []string{label}, Number: number})
	}
	// 完成项进入历史区；排序只影响展示，不改变 B/C 和平台队列。
	unfinished := func(p DetailContent) bool { return p.Status == "running" || p.Status == "queued" }
	orderTime := func(id int64) time.Time {
		if !started[id].IsZero() {
			return started[id]
		}
		return created[id]
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		left, right := result.Items[i], result.Items[j]
		if unfinished(left) != unfinished(right) {
			return !unfinished(left)
		}
		if started[left.PostID].IsZero() != started[right.PostID].IsZero() {
			return !started[left.PostID].IsZero()
		}
		return orderTime(left.PostID).Before(orderTime(right.PostID))
	})
	return result, nil
}

/* 历史资产：从任务/资源临时归组并推算整帖状态，现统一使用 B/C 关联投影。
// TaskDetails 与管理列表共用测试批次，Instagram 轮播按帖子归组。
func (a *App) TaskDetails(subscriptionID int64) (TaskDetails, error) {
	sub, err := a.Repo.Subscription(subscriptionID)
	if err != nil {
		return TaskDetails{}, err
	}
	settings, err := a.Repo.Settings()
	if err != nil {
		return TaskDetails{}, err
	}
	rows, err := a.Repo.TaskContentRows(subscriptionID, settings.Debug.TestItemLimit)
	if err != nil {
		return TaskDetails{}, err
	}
	result := TaskDetails{Platform: sub.Platform, Items: []DetailContent{}}
	index := map[string]int{}
	statuses := map[int64]string{}
	mediaNumbers := map[string]int{}
	createdTimes := map[string]time.Time{}
	startedTimes := map[string]time.Time{}
	options := settings.YouTube
	// 任务级选项整体覆盖全局默认值，与执行器的选项规则相同。
	if sub.YouTube != nil {
		options = *sub.YouTube
	}
	for _, row := range rows {
		task, content := row.Task, row.Content
		statuses[task.ID] = task.Status
		if task.ContentID == 0 {
			continue
		}
		key := content.RemoteID
		// 轮播媒体 ID 的尾部序号仅用于下载去重，展示归属于同一个帖子。
		if sub.Platform == "instagram" {
			key = strings.TrimSuffix(key, "-"+strconv.Itoa(content.Asset))
		}
		i, exists := index[key]
		if !exists {
			i = len(result.Items)
			index[key] = i
			result.Items = append(result.Items, DetailContent{Key: key, Title: content.Title, Status: task.Status, Parts: []DetailPart{}})
			createdTimes[key] = task.CreatedAt
		}
		// 已开始内容按本次执行时间排序；未开始内容按入队时间参与排列。
		// 重试会更新执行时间，重新开始的内容因此出现在列表下方。
		if !task.StartedAt.IsZero() && (startedTimes[key].IsZero() || task.StartedAt.Before(startedTimes[key])) {
			startedTimes[key] = task.StartedAt
		}
		group := &result.Items[i]
		if task.Status == "skipped" || (task.Status == "failed" && len(task.Progress) == 0) {
			group.Error = task.Error
		}
		if group.Title == "" {
			group.Title = key
		}
		appendPart := func(kind string, labels []string, number int) {
			// 未开始的附属项先显示待处理；引擎事件提供实际状态和字节数。
			p := domain.TransferProgress{Kind: kind, Status: "queued"}
			found := false
			for _, progress := range task.Progress {
				if progress.Kind == kind {
					p = progress
					found = true
					break
				}
			}
			if p.Title != "" {
				// 平台本地化标题在解析阶段即可显示，无需等待文件归位。
				group.Title = p.Title
			}
			if !found && task.Status == "completed" {
				// 缺少实时事件时查看真实产物，不将不存在的文件标为已完成。
				p = engine.ArtifactProgress(kind, content)
			}
			if task.Status == "failed" || task.Status == "cancelled" || task.Status == "skipped" {
				// 父任务终止后，尚未开始的附属项不可继续显示为待处理。
				if p.Status == "running" {
					p.Status = task.Status
					p.Error = task.Error
				} else if p.Status == "queued" {
					p.Status = "blocked"
				}
			}
			group.Parts = append(group.Parts, DetailPart{TransferProgress: p, TaskID: task.ID, Labels: labels, Number: number})
		}
		if sub.Platform == "instagram" {
			label := "照片"
			if content.MediaType == "video" || engine.Inventory(content, "").Video() != "" {
				label = "视频"
			}
			mediaNumbers[key+label]++
			// 照片和视频分别编号，同时保留媒体在帖子中的发现顺序。
			appendPart("media", []string{label}, mediaNumbers[key+label])
		} else {
			if options.Video || options.ExtractAudio {
				appendPart("video", []string{"视频"}, 0)
			}
			if options.Metadata || options.Comments {
				// 评论位于 info.json 中，两种标签表示同一个附属任务。
				labels := []string{}
				if options.Metadata {
					labels = append(labels, "元数据")
				}
				if options.Comments {
					labels = append(labels, "评论")
				}
				appendPart("metadata", labels, 0)
			}
			if options.Subtitles || options.AutoSubtitles {
				appendPart("subtitles", []string{"字幕"}, 0)
			}
			if options.Thumbnail {
				appendPart("thumbnail", []string{"缩略图"}, 0)
			}
			if options.ExtraAudio {
				appendPart("audio", []string{"音频"}, 0)
			}
			if options.ExtractAudio {
				appendPart("extracted", []string{"本地提取音频"}, 0)
			}
		}
	}
	for i := range result.Items {
		// 帖子在所有已发现媒体结束后完成；运行和待执行优先于终态。
		group := &result.Items[i]
		status := "completed"
		for _, part := range group.Parts {
			taskStatus := statuses[part.TaskID]
			if taskStatus == "running" {
				status = "running"
				break
			}
			if taskStatus == "queued" {
				status = "queued"
			} else if taskStatus == "failed" && status != "queued" {
				status = "failed"
			} else if taskStatus == "cancelled" && status == "completed" {
				status = "cancelled"
			} else if taskStatus == "skipped" && status == "completed" {
				status = "skipped"
			}
		}
		group.Status = status
	}
	orderTime := func(key string) time.Time {
		if started := startedTimes[key]; !started.IsZero() {
			return started
		}
		return createdTimes[key]
	}
	unfinished := func(item DetailContent) bool { return item.Status == "running" || item.Status == "queued" }
	// 结束项向上归入历史，未开始项留在当前执行项之后，不遮住下载中的内容。
	sort.SliceStable(result.Items, func(i, j int) bool {
		left, right := result.Items[i], result.Items[j]
		if unfinished(left) != unfinished(right) {
			return !unfinished(left)
		}
		if startedTimes[left.Key].IsZero() != startedTimes[right.Key].IsZero() {
			return !startedTimes[left.Key].IsZero()
		}
		return orderTime(left.Key).Before(orderTime(right.Key))
	})
	return result, nil
}

*/
