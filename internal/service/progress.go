package service

import (
	"encoding/json"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"strings"
)

// recordProgress 与取消、完成共用锁，进度事件不能恢复已取消的任务。
func (a *App) recordProgress(taskID int64, line string) bool {
	if !strings.HasPrefix(line, engine.ProgressPrefix) {
		return false
	}
	var progress domain.TransferProgress
	if json.Unmarshal([]byte(strings.TrimPrefix(line, engine.ProgressPrefix)), &progress) != nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	task, err := a.Repo.Task(taskID)
	if err != nil || task.Status != "running" {
		return true
	}
	for i, item := range task.Progress {
		if item.Kind == progress.Kind {
			if progress.Title == "" {
				progress.Title = item.Title
			}
			if progress.Status == "failed" {
				progress.BytesTotal = item.BytesTotal
				progress.BytesDone = item.BytesDone
				progress.Percent = item.Percent
			}
			task.Progress[i] = progress
			_ = a.Repo.SaveTask(&task)
			return true
		}
	}
	task.Progress = append(task.Progress, progress)
	_ = a.Repo.SaveTask(&task)
	return true
}
