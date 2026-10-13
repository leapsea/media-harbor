package service

import (
	"context"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loggingEngine 在清空操作两侧分别写入，验证打开的日志文件可继续使用。
type loggingEngine struct {
	fakeEngine
	ready  chan struct{}
	resume chan struct{}
}

func (e loggingEngine) Download(ctx context.Context, s domain.Subscription, c domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	log("before-clear")
	close(e.ready)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.resume:
	}
	log("after-clear")
	return e.fakeEngine.Download(ctx, s, c, o, dir, log)
}
func TestClearLogsWhileDownloading(t *testing.T) {
	e := loggingEngine{ready: make(chan struct{}), resume: make(chan struct{})}
	a := testApp(t, e)
	sub := addSub(t, a, "logs", "youtube")
	if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	// 无关联日志和其他配置文件分别创建，清空不操作配置。
	for _, name := range []string{"orphan.log", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(a.Config.LogDir, name), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.wg.Wait() }()
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("下载没有启动")
	}
	n, err := a.ClearLogs()
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	raw, err := os.ReadFile(taskLogPathForTest(t, a, task))
	if err != nil || len(raw) != 0 {
		t.Fatal(string(raw), err)
	}
	current, _ := a.Repo.Task(task.ID)
	if current.Status != "running" {
		t.Fatal(current)
	}
	close(e.resume)
	a.wg.Wait()
	raw, err = os.ReadFile(taskLogPathForTest(t, a, task))
	if err != nil || strings.Contains(string(raw), "before-clear") || !strings.Contains(string(raw), "after-clear") {
		t.Fatal(string(raw), err)
	}
	if _, err = os.Stat(filepath.Join(a.Config.LogDir, "orphan.log")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(a.Config.LogDir, "keep.txt")); err != nil {
		t.Fatal(err)
	}
}

// TestCleanWorkPreservesRetryCache 只清理无关联和完成目录，保留所有可续接产物。
func TestCleanWorkPreservesRetryCache(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "work", "youtube")
	paths := map[string]string{}
	for _, status := range []string{"queued", "running", "failed", "cancelled", "completed"} {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: status}); err != nil {
			t.Fatal(err)
		}
		tasks, _ := a.Repo.Tasks()
		task := tasks[0]
		task.Status = status
		if err := a.Repo.SaveTask(&task); err != nil {
			t.Fatal(err)
		}
		paths[status] = a.WorkPath(task)
	}
	paths["orphan"] = filepath.Join(a.Config.DownloadDir, ".work", "unassociated")
	for _, path := range paths {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "audio.m4a"), []byte("audio"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	media := filepath.Join(a.Config.DownloadDir, "video.mp4")
	if err := os.WriteFile(media, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	usage, err := a.StorageStatus()
	if err != nil || usage.Work.CleanableDirectories != 2 || usage.Work.RetainedDirectories != 4 || usage.Work.CleanableBytes != 10 {
		t.Fatal(usage, err)
	}
	n, err := a.CleanWork()
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	for status, path := range paths {
		_, err = os.Stat(path)
		if status == "completed" || status == "orphan" {
			if !os.IsNotExist(err) {
				t.Fatal(status, err)
			}
		} else if err != nil {
			t.Fatal(status, err)
		}
	}
	if _, err = os.Stat(media); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	if len(tasks) != 5 {
		t.Fatal(tasks)
	}
}
