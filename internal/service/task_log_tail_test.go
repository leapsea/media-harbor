package service

import (
	"fmt"
	"media-harbor/internal/domain"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestGroupLogTailAcrossDates 最近行跨日期按时间排序，空文件和末尾无换行都不改变数量。
func TestGroupLogTailAcrossDates(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "author", "youtube")
	settings, _ := app.Repo.Settings()
	settings.UI.TaskLogLines = 4
	if err := app.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	for offset, body := range map[int]string{-2: "old-01\nold-02", -1: "new-01\nnew-02\nnew-03", 0: ""} {
		if err := os.WriteFile(app.LogPath(sub, date.AddDate(0, 0, offset)), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	log, err := app.GroupLog(sub.ID)
	if err != nil || log != "old-02\nnew-01\nnew-02\nnew-03" {
		t.Fatal(log, err)
	}
}

// TestTailLogFileBlocks 多字节长行跨读取块，返回完整最近行，不保留旧行或截断中文。
func TestTailLogFileBlocks(t *testing.T) {
	path := t.TempDir() + "/large.log"
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%03d %s", i, strings.Repeat("中文", 300))
	}
	for _, ending := range []string{"", "\n"} {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+ending), 0644); err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, 20, 100, 200} {
			got, err := tailLogFile(path, limit)
			want := strings.Join(lines[max(0, len(lines)-limit):], "\n") + ending
			if err != nil || got != want || !utf8.ValidString(got) {
				t.Fatalf("limit=%d bytes=%d err=%v", limit, len(got), err)
			}
		}
	}
}

// TestGroupLogFlushesActiveWriter 读取窗口先刷新活跃日志缓冲，最新行无需等任务结束。
func TestGroupLogFlushesActiveWriter(t *testing.T) {
	app := testApp(t, fakeEngine{})
	t.Cleanup(app.closeLogs)
	sub := addSub(t, app, "author", "youtube")
	settings, _ := app.Repo.Settings()
	settings.UI.TaskLogLines = 2
	if err := app.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	writer, err := app.beginTaskLog(sub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		app.writeTaskLog(writer, domain.Task{ID: 1}, fmt.Sprintf("line-%d", i))
	}
	log, err := app.GroupLog(sub.ID)
	if err != nil || strings.Count(log, "\n") != 2 || strings.Contains(log, "line-1") || !strings.Contains(log, "line-3") {
		t.Fatal(log, err)
	}
}
