package service

import (
	"fmt"
	"media-harbor/internal/domain"
	"os"
	"strings"
	"time"
)

// paidChannel 使用内容的频道身份，主页的稳定频道 ID 用于尚未补齐的条目。
// 播放列表 ID、视频 ID 和显示名称不能作为 UP 主的付费策略键。
func paidChannel(content domain.Content, sub domain.Subscription) string {
	if content.ChannelID != "" {
		return content.ChannelID
	}
	if domain.TaskMode(sub.Platform, sub.URL) == "profile" && strings.HasPrefix(sub.StableID, "UC") {
		return sub.StableID
	}
	return ""
}

// skipBlockedPaidLocked 在认领下载名额之前处理付费条目，调用者持有应用锁。
func (a *App) skipBlockedPaidLocked(task *domain.Task, sub domain.Subscription) (bool, error) {
	if sub.Platform != "youtube" {
		return false, nil
	}
	content, err := a.Repo.Content(task.ContentID)
	if err != nil {
		return false, err
	}
	if !domain.IsPaidAvailability(content.Availability) {
		return false, nil
	}
	channel := paidChannel(content, sub)
	if channel == "" {
		return false, nil
	}
	failed, err := a.Repo.PaidFailure(channel)
	if err != nil || failed == 0 {
		return false, err
	}
	reason := (&domain.PaidVideoSkippedError{ChannelID: channel, FailedTaskID: failed}).Error()
	task.Status = "skipped"
	task.Error = reason
	task.FinishedAt = time.Now()
	task.Progress = []domain.TransferProgress{{Kind: "video", Status: "skipped"}}
	content.Status = "skipped"
	content.Error = reason
	if err = a.Repo.SaveContent(&content); err != nil {
		return false, err
	}
	if err = a.Repo.SaveTask(task); err != nil {
		return false, err
	}
	// 尚未启动引擎的事件仍写入挂载日志，记录媒体及首次失败来源。
	a.logMu.Lock()
	defer a.logMu.Unlock()
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if task.Attempts == 0 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	file, err := os.OpenFile(a.LogPath(*task), flags, 0644)
	if err != nil {
		return true, err
	}
	defer file.Close()
	_, err = fmt.Fprint(file, taskLogLine(task.FinishedAt, "媒体 "+content.RemoteID+"；"+reason))
	return true, err
}
