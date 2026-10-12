package engine

import (
	"fmt"
	"media-harbor/internal/config"
	"strings"
	"sync"
	"time"
)

// commandLog 在 stdout/stderr 之间统一限制进度频率。
// 最终汇总、警告和错误即时写入；100% 的中间估算仍视为进度。
type commandLog struct {
	mu           sync.Mutex
	log          func(string)
	now          func() time.Time
	lastProgress time.Time
	lastError    string
}

func (c *commandLog) line(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	progress := strings.HasPrefix(line, "[download]") && strings.Contains(line, "%") && strings.Contains(line, "ETA")
	progress = progress || strings.HasPrefix(line, "frame=") || (strings.HasPrefix(line, "size=") && strings.Contains(line, "time="))
	if progress {
		now := c.now()
		if !c.lastProgress.IsZero() && now.Sub(c.lastProgress) < config.DownloadLogInterval {
			return
		}
		c.lastProgress = now
	}
	if strings.HasPrefix(line, "ERROR:") {
		c.lastError = line
	}
	c.log(line)
}

// logWriter 按完整行输出，兼容被分块写入和回车覆盖的进度。
// 每个进程输出流独立缓存，避免 stderr 与 stdout 拼接成一条日志。
type logWriter struct {
	output  *commandLog
	pending string
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.pending += string(p)
	for {
		i := strings.IndexAny(w.pending, "\r\n")
		if i < 0 {
			break
		}
		w.output.line(w.pending[:i])
		w.pending = w.pending[i+1:]
	}
	return len(p), nil
}
func (w *logWriter) flush() { w.output.line(w.pending); w.pending = "" }

// youtubeDownloadError 保留原始错误链，明确失败阶段和流进度语义。
func youtubeDownloadError(stage string, err error) error {
	if strings.Contains(err.Error(), "403") {
		return fmt.Errorf("YouTube %s失败：平台拒绝媒体请求（HTTP 403）；单个流达到 100%% 不代表任务完成，可重试以重新获取媒体地址：%w", stage, err)
	}
	return fmt.Errorf("YouTube %s失败：%w", stage, err)
}
