package storage

import (
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestClearDownloadHistory 保留完整下载索引、队列与文件，清空状态跨服务重开保持。
func TestClearDownloadHistory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "media.db")
	s, err := Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Platform: "youtube", URL: "https://example.test/author"}
	if err := s.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	if err := s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Contents()
	item := items[0]
	file := filepath.Join(root, "one.mp4")
	if err := os.WriteFile(file, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	item.Status, item.Paths = "completed", []string{file}
	if err := s.SaveContent(&item); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks()
	count, err := s.ClearDownloadHistory()
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	count, err = s.ClearDownloadHistory()
	if err != nil || count != 0 {
		t.Fatal(count, err)
	}
	s.Close()
	s, err = Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	history, _ := s.DownloadHistory()
	saved, _ := s.Content(item.ID)
	after, _ := s.Tasks()
	if len(history) != 0 || !reflect.DeepEqual(saved, item) || !reflect.DeepEqual(tasks, after) {
		t.Fatal(history, saved, after)
	}
	raw, err := os.ReadFile(file)
	if err != nil || string(raw) != "media" {
		t.Fatal(string(raw), err)
	}
	// 重复发现与归档状态更新不得恢复清空的历史。
	if err := s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	item.Archived = true
	if err := s.SaveContent(&item); err != nil {
		t.Fatal(err)
	}
	history, _ = s.DownloadHistory()
	if len(history) != 0 {
		t.Fatal(history)
	}
	if err := s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "two"}); err != nil {
		t.Fatal(err)
	}
	history, _ = s.DownloadHistory()
	if len(history) != 1 || history[0].RemoteID != "two" {
		t.Fatal(history)
	}
	// 明确的新下载执行可以重新进入历史，索引身份保持不变。
	if err := s.ShowDownloadHistory(item.ID); err != nil {
		t.Fatal(err)
	}
	history, _ = s.DownloadHistory()
	if len(history) != 2 {
		t.Fatal(history)
	}
	page, err := s.DownloadHistoryPage(1, 1)
	total, countErr := s.DownloadHistoryCount()
	if err != nil || countErr != nil || len(page) != 1 || page[0].RemoteID != "one" || total != 2 {
		t.Fatal(page, total, err, countErr)
	}
}
