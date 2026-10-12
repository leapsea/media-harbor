package service

import (
	"fmt"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"time"
)

// LogPath 以不可变的创建时间和任务编号定位日志，数据库重建复用编号也不会串用文件。
// UTC 纳秒时间确保路径不受本地时区切换影响，重试仍写入同一任务日志。
func (a *App) LogPath(task domain.Task) string {
	name := taskKey(task) + ".log"
	return filepath.Join(a.Config.LogDir, name)
}

// taskLogLine 为执行事件和未执行状态统一添加完整本地日期与时间。
func taskLogLine(at time.Time, message string) string {
	return fmt.Sprintf("[%s] %s\n", at.Local().Format("2006-01-02 15:04:05"), message)
}

// TaskLog 与聚合日志共用任务身份及读取规则。
func (a *App) TaskLog(id int64) (string, error) {
	task, err := a.Repo.Task(id)
	if err != nil {
		return "", err
	}
	return a.taskLog(task)
}

// taskLog 未执行时使用创建或取消时间描述状态，不读取磁盘中的其他任务日志。
func (a *App) taskLog(task domain.Task) (string, error) {
	// 策略跳过没有新的下载尝试，展示本次事件，避免把旧失败日志当成本次执行。
	if task.Status == "skipped" {
		return taskLogLine(task.FinishedAt, task.Error), nil
	}
	if task.Attempts == 0 {
		if task.Status == "cancelled" {
			return taskLogLine(task.FinishedAt, "未开始执行，已取消"), nil
		}
		return taskLogLine(task.CreatedAt, "等待执行，尚未生成日志"), nil
	}
	raw, err := os.ReadFile(a.LogPath(task))
	if os.IsNotExist(err) {
		return taskLogLine(task.StartedAt, "任务尚未生成日志"), nil
	}
	return string(raw), err
}

// taskKey 是任务创建时确定的磁盘身份，重试复用，新库同编号保持隔离。
func taskKey(task domain.Task) string {
	return fmt.Sprintf("%s-%d", task.CreatedAt.UTC().Format("20060102T150405.000000000Z"), task.ID)
}

// WorkPath 只定位当前任务临时文件，不读取旧编号目录。
func (a *App) WorkPath(task domain.Task) string {
	return filepath.Join(a.Config.DownloadDir, ".work", taskKey(task))
}
