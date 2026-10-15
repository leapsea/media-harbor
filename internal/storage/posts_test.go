package storage

import (
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"path/filepath"
	"testing"
)

// postStore 装配真实 SQLite，测试期望清单与事务聚合而非重复实现聚合算法。
func postStore(t testing.TB, platform string) (*Store, domain.Subscription) {
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

// 三个资源只写入两个时，即使两个都完成，帖子任务也必须保持未完成。
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
	// 重复发现不创建第二个帖子任务与下载资源任务，也不撤销已完成的子资源。
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
		t.Fatal("文件归位前不能完成帖子任务", p)
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

// 恢复执行只清理停止说明，已完成的视频、待执行项的顺序及真实失败仍可追溯。
func TestResumedResourcesClearOnlyRestartWait(t *testing.T) {
	for _, reason := range []string{"服务停止，等待继续执行", "服务重启，等待继续执行"} {
		t.Run(reason, func(t *testing.T) {
			s, sub := postStore(t, "youtube")
			sub.YouTube = &domain.YouTubeOptions{Video: true, Comments: true, Subtitles: true}
			if err := s.SaveSubscription(&sub); err != nil {
				t.Fatal(err)
			}
			if err := s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "resume"}); err != nil {
				t.Fatal(err)
			}
			tasks, _ := s.SubscriptionTasks(sub.ID)
			task := tasks[0]
			task.Status, task.Error = "queued", reason
			task.Progress = []domain.TransferProgress{{Kind: "video", Status: "completed", BytesTotal: 1024, BytesDone: 1024, Percent: 100}}
			if err := s.SaveTask(&task); err != nil {
				t.Fatal(err)
			}
			// 同一视频的另一附属项包含真实错误，不能被重启文案的清理规则抹掉。
			post, _ := s.Post(sub.ID, "resume")
			if _, err := s.db.Exec(`UPDATE download_items SET payload=json_set(payload,'$.error','HTTP 403: Forbidden') WHERE post_id=? AND kind='subtitles'`, post.ID); err != nil {
				t.Fatal(err)
			}
			task.Status, task.Error, task.Progress = "running", "", nil
			if err := s.SaveTask(&task); err != nil {
				t.Fatal(err)
			}
			parts, err := s.DownloadItems(post.ID)
			if err != nil || len(parts) != 3 {
				t.Fatal(parts, err)
			}
			for _, part := range parts {
				switch part.Kind {
				case "video":
					if part.Status != "completed" || part.Progress.BytesDone != 1024 {
						t.Fatal("已完成的视频被重置", part)
					}
				case "comments":
					if part.Status != "queued" || part.Error != "" || part.Progress.Error != "" {
						t.Fatal("评论应保持待执行且清除停止说明", part)
					}
				case "subtitles":
					if part.Status != "queued" || part.Error != "HTTP 403: Forbidden" {
						t.Fatal("真实失败原因被清理", part)
					}
				}
			}
			post, _ = s.Post(sub.ID, "resume")
			if post.Status != "running" || post.Error != "HTTP 403: Forbidden" || post.Completed != 1 {
				t.Fatal("父任务应保留真实错误与完成计数", post)
			}
		})
	}
}
