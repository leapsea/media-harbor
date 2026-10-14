package service

import (
	"media-harbor/internal/domain"
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

// TaskDetails 从 B/C 关联读取领域状态；执行账本只提供操作 ID 与执行排序。
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
		if p.Title != "" {
			result.Items[i].Title = p.Title
		}
		label, number := labels[resource.Kind], 0
		if resource.Kind == "media" {
			label = "照片"
			if row.Content.MediaType == "video" {
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
