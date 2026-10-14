package storage

import (
	"encoding/json"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"path/filepath"
	"testing"
)

// postStore 装配真实 SQLite，测试期望清单与事务聚合而非重复实现聚合算法。
func postStore(t *testing.T, platform string) (*Store, domain.Subscription) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	sub := domain.Subscription{Platform: platform, URL: "https://example.test/author"}
	if err = s.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	return s, sub
}

// 三个资源只写入两个时，即使两个都完成，B 也必须保持未完成。
func TestPostManifestRequiresEveryExpectedResource(t *testing.T) {
	s, sub := postStore(t, "instagram")
	for num := 1; num <= 2; num++ {
		item := domain.Content{RemoteID: []string{"pk-1", "pk-2"}[num-1], URL: "https://www.instagram.com/p/post/", Asset: num, ExpectedAssets: 3}
		if err := s.AddContentAndTask(sub.ID, item); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := s.SubscriptionTasks(sub.ID)
	for _, task := range tasks {
		task.Status = "completed"
		if err := s.SaveTask(&task); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.Post(sub.ID, "post")
	if err != nil || p.Status == "completed" || p.Expected != 3 || p.Completed != 2 {
		t.Fatalf("premature post=%+v err=%v", p, err)
	}
	if err = s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "pk-3", URL: p.URL, Asset: 3, ExpectedAssets: 3}); err != nil {
		t.Fatal(err)
	}
	tasks, _ = s.SubscriptionTasks(sub.ID)
	task := tasks[0]
	task.Status = "running"
	_ = s.SaveTask(&task)
	p, _ = s.Post(sub.ID, "post")
	if p.Status != "running" {
		t.Fatal(p)
	}
	task.Status = "completed"
	_ = s.SaveTask(&task)
	p, _ = s.Post(sub.ID, "post")
	if p.Status != "completed" || p.Completed != 3 {
		t.Fatal(p)
	}
	resources, _ := s.DownloadItems(p.ID)
	if len(resources) != 3 {
		t.Fatal(resources)
	}
	// 重复发现不创建第二个 B/C，也不撤销已完成的子资源。
	if _, err = s.RegisterPost(sub.ID, "instagram", domain.Content{RemoteID: "pk-1", URL: p.URL, Asset: 1, ExpectedAssets: 3}, domain.YouTubeOptions{}); err != nil {
		t.Fatal(err)
	}
	resources, _ = s.DownloadItems(p.ID)
	if len(resources) != 3 || resources[0].Status != "completed" {
		t.Fatal(resources)
	}
}

// 一个视频的媒体、评论文件和字幕各自持久化；失败和取消保留已完成的视频。
func TestVideoChildrenSurviveFailureCancelAndRetry(t *testing.T) {
	s, sub := postStore(t, "youtube")
	sub.YouTube = &domain.YouTubeOptions{Video: true, Comments: true, Subtitles: true}
	_ = s.SaveSubscription(&sub)
	if err := s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "video", URL: "https://youtu.be/video"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.SubscriptionTasks(sub.ID)
	task := tasks[0]
	task.Status = "running"
	task.Progress = []domain.TransferProgress{{Kind: "video", Status: "completed"}, {Kind: "metadata", Status: "failed", Error: "comment failure"}}
	_ = s.SaveTask(&task)
	p, _ := s.Post(sub.ID, "video")
	if p.Status == "completed" || p.Completed != 1 {
		t.Fatal(p)
	}
	task.Status = "failed"
	_ = s.SaveTask(&task)
	p, _ = s.Post(sub.ID, "video")
	if p.Status != "failed" {
		t.Fatal(p)
	}
	task.Status = "queued"
	task.Progress = nil
	_ = s.SaveTask(&task)
	if _, err := s.CancelSubscriptionTasks(sub.ID); err != nil {
		t.Fatal(err)
	}
	children, _ := s.DownloadItems(p.ID)
	if children[0].Status != "completed" || children[1].Status != "cancelled" {
		t.Fatal(children)
	}
	task.Status = "running"
	task.Progress = []domain.TransferProgress{{Kind: "video", Status: "completed"}, {Kind: "metadata", Status: "completed"}, {Kind: "subtitles", Status: "skipped"}}
	_ = s.SaveTask(&task)
	p, _ = s.Post(sub.ID, "video")
	if p.Status == "completed" {
		t.Fatal("文件归位前不能完成 B", p)
	}
	task.Status = "completed"
	_ = s.SaveTask(&task)
	p, _ = s.Post(sub.ID, "video")
	if p.Status != "completed" || p.Completed != 3 {
		t.Fatal(p)
	}
	children, _ = s.DownloadItems(p.ID)
	if children[2].Outcome != "unavailable" {
		t.Fatal(children)
	}
	// 新启用的附属项进入期望集合，不能继承视频的完成状态。
	sub.YouTube.Thumbnail = true
	_ = s.SaveSubscription(&sub)
	items, _ := s.Contents()
	if _, err := s.RegisterPost(sub.ID, "youtube", items[0], *sub.YouTube); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Post(sub.ID, "video")
	if p.Expected != 4 || p.Status == "completed" {
		t.Fatal(p)
	}
}

// 升级保留旧任务/资源 ID 与文件位置；旧轮播清单未知，重新确认后才能完成。
func TestPostMigrationPreservesOldIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	s, err := Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Platform: "instagram", URL: "https://example.test/author"}
	_ = s.SaveSubscription(&sub)
	_ = s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "pk-1", URL: "https://www.instagram.com/p/old/", Asset: 1})
	items, _ := s.Contents()
	item := items[0]
	item.Status = "completed"
	item.Paths = []string{"/downloads/kept.jpg"}
	item.PostID = 0
	raw, _ := json.Marshal(item)
	_, err = s.db.Exec("UPDATE content_items SET payload=? WHERE id=?", raw, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.Tasks()
	task := tasks[0]
	task.Status = "completed"
	_ = s.SaveTask(&task)
	_, _ = s.db.Exec("DELETE FROM download_items")
	_, _ = s.db.Exec("DELETE FROM post_tasks")
	s.Close()
	s, err = Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrated, _ := s.Content(item.ID)
	p, _ := s.Post(sub.ID, "old")
	children, _ := s.DownloadItems(p.ID)
	if migrated.ID != item.ID || len(migrated.Paths) != 1 || p.ManifestComplete || p.Status == "completed" || len(children) != 1 || children[0].Status != "completed" {
		t.Fatalf("content=%+v post=%+v children=%+v", migrated, p, children)
	}
	migrated.ExpectedAssets = 1
	_, err = s.RegisterPost(sub.ID, "instagram", migrated, domain.YouTubeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = s.Post(sub.ID, "old")
	if p.Status != "completed" {
		t.Fatal(p)
	}
}

// 旧的明确跳过事件在迁移后仍表示平台不提供资源，详情不能改显示为有文件完成。
func TestPostMigrationReplaysUnavailableChild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	s, err := Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Platform: "youtube", URL: "https://example.test/author", YouTube: &domain.YouTubeOptions{Video: true, Subtitles: true}}
	_ = s.SaveSubscription(&sub)
	_ = s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "old-video"})
	tasks, _ := s.Tasks()
	task := tasks[0]
	task.Status = "completed"
	task.Progress = []domain.TransferProgress{{Kind: "video", Status: "completed", BytesTotal: 100, BytesDone: 100}, {Kind: "subtitles", Status: "skipped"}}
	_ = s.SaveTask(&task)
	// 模拟前一版已关联 B/C 但丢失子项快照的升级现场。
	_, err = s.db.Exec("UPDATE download_items SET payload=json_remove(payload,'$.progress','$.outcome')")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, _ := s.Post(sub.ID, "old-video")
	children, _ := s.DownloadItems(p.ID)
	if children[0].Progress.BytesTotal != 100 || children[1].Outcome != "unavailable" || p.Status != "completed" {
		t.Fatalf("post=%+v children=%+v", p, children)
	}
}
