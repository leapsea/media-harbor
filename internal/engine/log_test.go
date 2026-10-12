package engine

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCommandLog 限制密集进度，同时保留最终汇总、阶段和错误。
// 分块写入及回车行必须被组装成完整日志，stdout/stderr 共享频率。
func TestCommandLog(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	var lines []string
	output := &commandLog{log: func(s string) { lines = append(lines, s) }, now: func() time.Time { return now }}
	stdout := &logWriter{output: output}
	stderr := &logWriter{output: output}
	stdout.Write([]byte("[download] 10"))
	stdout.Write([]byte("% of 20MiB ETA 00:10\r[download] 11% of 20MiB ETA 00:09\n"))
	stderr.Write([]byte("[download] 12% of 20MiB ETA 00:08\n"))
	if len(lines) != 1 {
		t.Fatal(lines)
	}
	now = now.Add(time.Second)
	stdout.Write([]byte("[download] 100.0% of ~ 20MiB ETA 00:01\n[download] 100% of 20MiB in 00:00:10\n"))
	stderr.Write([]byte("ERROR: HTTP Error 403: Forbidden"))
	stderr.flush()
	if len(lines) != 4 || !strings.Contains(lines[2], "in 00:00:10") || !strings.Contains(lines[3], "403") {
		t.Fatal(lines)
	}
	if output.lastError != lines[3] {
		t.Fatal(output.lastError)
	}
	cause := errors.New("HTTP Error 403: Forbidden")
	err := youtubeDownloadError("主视频及音轨", cause)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "不代表任务完成") {
		t.Fatal(err)
	}
}
