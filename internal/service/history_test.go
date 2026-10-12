package service

import (
	"context"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

// TestHistoryClearKeepsScanIndex 验证扫描保留去重索引，只有视频且文件完整时补全跳过。
func TestHistoryClearKeepsScanIndex(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "author", "youtube")
	if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	contents, _ := a.Repo.Contents()
	completed := contents[0]
	completed.Status = "completed"
	path := filepath.Join(t.TempDir(), "one.mp4")
	_ = os.WriteFile(path, []byte("media"), 0644)
	completed.Paths = []string{path}
	settings, _ := a.Repo.Settings()
	settings.YouTube = domain.YouTubeOptions{Video: true}
	_ = a.SaveSettings(settings)
	if err := a.Repo.SaveContent(&completed); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	tasks[0].Status = "completed"
	if err := a.Repo.SaveTask(&tasks[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ClearDownloadHistory(); err != nil {
		t.Fatal(err)
	}
	for _, catchup := range []bool{false, true} {
		added, retried, done, _, skipped, err := a.reconcileScanLocked(sub.ID, []domain.Content{completed}, catchup)
		if err != nil || added != 0 || retried != 0 || (catchup && done != 1) || (!catchup && skipped != 1) {
			t.Fatal(added, retried, done, skipped, err)
		}
	}
	history, _ := a.ContentHistory()
	after, _ := a.Repo.Tasks()
	if len(history) != 0 || len(after) != len(tasks) {
		t.Fatal(history, after)
	}
}

// TestClearedFailureRetry 返回历史的是新一次下载执行，而不是普通轮询或补全扫描。
func TestClearedFailureRetry(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "author", "youtube")
	if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	contents, _ := a.Repo.Contents()
	item := contents[0]
	item.Status = "failed"
	a.Repo.SaveContent(&item)
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	task.Status = "failed"
	a.Repo.SaveTask(&task)
	if _, err := a.ClearDownloadHistory(); err != nil {
		t.Fatal(err)
	}
	_, retried, _, _, _, err := a.reconcileScanLocked(sub.ID, []domain.Content{item}, true)
	if err != nil || retried != 1 {
		t.Fatal(retried, err)
	}
	task, _ = a.Repo.Task(task.ID)
	task.Status = "running"
	a.Repo.SaveTask(&task)
	settings, _ := a.Repo.Settings()
	a.execute(context.Background(), task, sub, settings)
	history, _ := a.ContentHistory()
	if len(history) != 1 || history[0].ID != item.ID || history[0].Status != "completed" {
		t.Fatal(history)
	}
}
