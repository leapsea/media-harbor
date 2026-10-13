package service

import (
	"bufio"
	"bytes"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// subscriptionLogWriter 将同一订阅的任务追加到按日期划分的共享日志中。
// App.logMu 保护文件句柄、缓冲区与引用数，多个任务并发时也不会交错写入单行。
type subscriptionLogWriter struct {
	file     *os.File
	buffer   *bufio.Writer
	users    int
	lastUsed time.Time
}

const logWriterIdleTimeout = time.Minute

var invalidLogName = regexp.MustCompile(`[^\pL\pN._-]+`)

// taskLogLine 为不落盘的模拟运行日志统一添加本地时间。
func taskLogLine(at time.Time, message string) string {
	return fmt.Sprintf("[%s] %s\n", at.Local().Format("2006-01-02 15:04:05"), message)
}

// taskKey 仍用于下载工作目录，不再用于任务日志文件命名。
func taskKey(task domain.Task) string {
	return fmt.Sprintf("%s-%d", task.CreatedAt.UTC().Format("20060102T150405.000000000Z"), task.ID)
}

// WorkPath 只定位当前任务临时文件，不读取旧编号目录。
func (a *App) WorkPath(task domain.Task) string {
	return filepath.Join(a.Config.DownloadDir, ".work", taskKey(task))
}

// logNamePrefix 保留可检索的中文任务名，并移除路径分隔符及文件系统特殊字符。
func logNamePrefix(name string) string {
	name = strings.TrimSpace(name)
	name = invalidLogName.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return "任务"
	}
	return name
}

// LogPath 按任务名称和执行日期定位共享日志文件。
func (a *App) LogPath(sub domain.Subscription, at time.Time) string {
	name := fmt.Sprintf("%s_%s.log", logNamePrefix(sub.DisplayName), at.Local().Format("20060102"))
	return filepath.Join(a.Config.LogDir, name)
}

// beginTaskLog 复用同一订阅当天的文件句柄，并记录并发使用者数量。
func (a *App) beginTaskLog(sub domain.Subscription, at time.Time) (*subscriptionLogWriter, error) {
	path := a.LogPath(sub, at)
	a.logMu.Lock()
	defer a.logMu.Unlock()
	if writer := a.loggers[path]; writer != nil {
		writer.users++
		writer.lastUsed = time.Now()
		return writer, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	writer := &subscriptionLogWriter{file: file, buffer: bufio.NewWriterSize(file, 16*1024), users: 1, lastUsed: time.Now()}
	a.loggers[path] = writer
	return writer, nil
}

// writeTaskLog 为每行保留任务身份，避免平台允许并发下载时过程内容互相混淆。
func (a *App) writeTaskLog(writer *subscriptionLogWriter, task domain.Task, message string) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(message, "\n"), "\n") {
		if _, err := fmt.Fprintf(writer.buffer, "%s [任务 #%d] %s\n", time.Now().Local().Format("[2006-01-02 15:04:05]"), task.ID, line); err != nil {
			return
		}
	}
	writer.lastUsed = time.Now()
}

// endTaskLog 在任务结束时落盘，并保留句柄供同订阅后续任务复用。
func (a *App) endTaskLog(sub domain.Subscription, at time.Time, writer *subscriptionLogWriter) {
	path := a.LogPath(sub, at)
	a.logMu.Lock()
	defer a.logMu.Unlock()
	if current := a.loggers[path]; current != writer {
		return
	}
	writer.users--
	_ = writer.buffer.Flush()
	writer.lastUsed = time.Now()
}

// flushLogs 将执行中的任务日志按调度周期落盘，进程停止时仍由 endTaskLog 兜底刷新。
func (a *App) flushLogs() {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	for path, writer := range a.loggers {
		_ = writer.buffer.Flush()
		if writer.users == 0 && time.Since(writer.lastUsed) >= logWriterIdleTimeout {
			_ = writer.file.Close()
			delete(a.loggers, path)
		}
	}
}

// closeLogs 在调度器停止且任务协程退出后刷新并释放所有日志句柄。
func (a *App) closeLogs() {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	for path, writer := range a.loggers {
		_ = writer.buffer.Flush()
		_ = writer.file.Close()
		delete(a.loggers, path)
	}
}

// writeSubscriptionLog 写入调度器中尚未启动引擎的结果事件。
func (a *App) writeSubscriptionLog(sub domain.Subscription, task domain.Task, at time.Time, message string) error {
	writer, err := a.beginTaskLog(sub, at)
	if err != nil {
		return err
	}
	a.writeTaskLog(writer, task, message)
	a.endTaskLog(sub, at, writer)
	return nil
}

// TaskLog 只返回该任务在新式订阅日志中的行，不兼容旧的任务独立日志文件。
func (a *App) TaskLog(id int64) (string, error) {
	task, err := a.Repo.Task(id)
	if err != nil {
		return "", err
	}
	sub, err := a.Repo.Subscription(task.SubscriptionID)
	if err != nil {
		return "", err
	}
	logs, err := a.readSubscriptionLogs(sub)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	marker := fmt.Sprintf("[任务 #%d]", task.ID)
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, marker) {
			result.WriteString(line)
			result.WriteByte('\n')
		}
	}
	return result.String(), nil
}

// readSubscriptionLogs 读取当前订阅的共享日志；活跃缓冲先刷新，读取过程与写入互斥。
func (a *App) readSubscriptionLogs(sub domain.Subscription) (string, error) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	for _, writer := range a.loggers {
		_ = writer.buffer.Flush()
	}
	pattern := filepath.Join(a.Config.LogDir, logNamePrefix(sub.DisplayName)+"_????????.log")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	var result strings.Builder
	for _, path := range paths {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		result.Write(raw)
	}
	return result.String(), nil
}

// readSubscriptionLogTail 从最新日期开始读取，凑齐行数后停止，再按时间顺序返回。
// 磁盘日志保留完整内容，读取上限只影响日志窗口及其接口。
func (a *App) readSubscriptionLogTail(sub domain.Subscription, limit int) (string, error) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	paths, err := filepath.Glob(filepath.Join(a.Config.LogDir, logNamePrefix(sub.DisplayName)+"_????????.log"))
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	parts := []string{}
	remaining := limit
	for i := len(paths) - 1; i >= 0 && remaining > 0; i-- {
		if writer := a.loggers[paths[i]]; writer != nil {
			if err := writer.buffer.Flush(); err != nil {
				return "", err
			}
		}
		raw, err := tailLogFile(paths[i], remaining)
		if err != nil {
			return "", err
		}
		parts = append(parts, raw)
		remaining -= strings.Count(raw, "\n")
		if raw != "" && !strings.HasSuffix(raw, "\n") {
			remaining--
		}
	}
	var result strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "" {
			continue
		}
		// 日期文件之间保持行边界，上一文件末行没有换行符时也不拼接两条记录。
		if result.Len() > 0 && !strings.HasSuffix(result.String(), "\n") {
			result.WriteByte('\n')
		}
		result.WriteString(parts[i])
	}
	return result.String(), nil
}

// tailLogFile 从文件末尾按块读取；先找到完整行边界，再截取，避免切断 UTF-8。
// 最后一行没有换行符时也算一行，结尾换行符不额外算作空行。
func tailLogFile(path string, limit int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset, newlines := info.Size(), 0
	chunks := [][]byte{}
	for offset > 0 && newlines <= limit {
		size := min(offset, int64(config.LogTailReadChunkBytes))
		offset -= size
		chunk := make([]byte, int(size))
		if _, err := file.ReadAt(chunk, offset); err != nil {
			return "", err
		}
		chunks = append(chunks, chunk)
		newlines += bytes.Count(chunk, []byte{'\n'})
	}
	var raw bytes.Buffer
	for i := len(chunks) - 1; i >= 0; i-- {
		raw.Write(chunks[i])
	}
	data := raw.Bytes()
	end := len(data)
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	lines := 1
	for i := end - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lines++
			if lines > limit {
				return string(data[i+1:]), nil
			}
		}
	}
	return string(data), nil
}
