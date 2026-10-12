package storage

import (
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"path/filepath"
	"reflect"
	"testing"
)

// TestContentDedup 验证重复扫描不会创建第二份内容或下载任务。
func TestContentDedup(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// 两次发现使用同一订阅和平台内容 ID。
	sub := domain.Subscription{Platform: "youtube", URL: "https://youtube.com/@example"}
	if e = s.SaveSubscription(&sub); e != nil {
		t.Fatal(e)
	}
	c := domain.Content{RemoteID: "video-1", URL: "https://youtube.com/watch?v=video-1"}
	// 重复扫描应由唯一索引转为无操作。
	for range 2 {
		if e = s.AddContentAndTask(sub.ID, c); e != nil {
			t.Fatal(e)
		}
	}
	// 内容与任务必须同时创建，数量应保持一对一。
	items, e := s.Contents()
	if e != nil || len(items) != 1 {
		t.Fatalf("contents=%v error=%v", items, e)
	}
	tasks, e := s.Tasks()
	if e != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%v error=%v", tasks, e)
	}
	// 已完成状态不会因下一轮扫描被重置。
	items[0].Status = "completed"
	if e = s.SaveContent(&items[0]); e != nil {
		t.Fatal(e)
	}
	if e = s.AddContentAndTask(sub.ID, c); e != nil {
		t.Fatal(e)
	}
	tasks, _ = s.Tasks()
	if len(tasks) != 1 {
		t.Fatal("已完成内容重复排队")
	}
}

// TestRestartRecovery 验证未完成任务在重开数据库后恢复为待执行。
func TestRestartRecovery(t *testing.T) {
	// 使用同一文件关闭并重开，验证真实持久化恢复。
	path := filepath.Join(t.TempDir(), "media.db")
	s, e := Open(path, config.Defaults())
	if e != nil {
		t.Fatal(e)
	}
	task, e := s.Enqueue(1, 0, "scan")
	if e != nil {
		t.Fatal(e)
	}
	// 模拟进程在引擎调用期间退出。
	task.Status = "running"
	if e = s.SaveTask(&task); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(path, config.Defaults())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	task, e = s.Task(task.ID)
	if e != nil || task.Status != "queued" {
		t.Fatalf("task=%v error=%v", task, e)
	}
}

// TestStableIdentity 验证相同平台稳定 ID 不可绑定两个订阅。
func TestStableIdentity(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// 用户名可变，但平台稳定 ID 不变。
	first := domain.Subscription{Platform: "instagram", URL: "https://instagram.com/alice", StableID: "123"}
	if e = s.SaveSubscription(&first); e != nil {
		t.Fatal(e)
	}
	// 新 URL 不能绕过稳定身份唯一约束。
	second := domain.Subscription{Platform: "instagram", URL: "https://instagram.com/alice_new", StableID: "123"}
	if s.SaveSubscription(&second) == nil {
		t.Fatal("重复身份被接受")
	}
}

// TestSettingsPersistence 当前完整配置首次初始化、保存和重开后保持一致。
func TestSettingsPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media.db")
	defaults := config.Defaults()
	s, err := Open(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings()
	if err != nil || !reflect.DeepEqual(settings, defaults) {
		t.Fatal(settings, err)
	}
	settings.UseUploadDate = false
	settings.UI.Language = "en"
	settings.UI.HistoryPageSize = 5
	settings.UI.RecentDownloads = 20
	if err = s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, err := s.Settings()
	if err != nil || !reflect.DeepEqual(saved, settings) {
		t.Fatal(saved, err)
	}
}

// TestContentAndProfileIdentity 同一作者的多个帖子与主页可共存，重复主页仍拒绝。
func TestContentAndProfileIdentity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, platform := range []string{"instagram", "youtube"} {
		profileURL := "https://www.instagram.com/author/"
		urls := []string{"https://www.instagram.com/p/post-one/", "https://www.instagram.com/p/post-two/"}
		if platform == "youtube" {
			profileURL = "https://www.youtube.com/@author"
			urls = []string{"https://www.youtube.com/watch?v=one", "https://www.youtube.com/watch?v=two"}
		}
		// 先保存详情并更新扫描得到的作者 ID，重现真实触发顺序。
		for _, address := range urls {
			item := domain.Subscription{Platform: platform, URL: address}
			if err := s.SaveSubscription(&item); err != nil {
				t.Fatal(err)
			}
			item.StableID = "author-id"
			if err := s.SaveSubscription(&item); err != nil {
				t.Fatal(err)
			}
			saved, err := s.Subscription(item.ID)
			if err != nil || saved.StableID != "author-id" {
				t.Fatal(saved, err)
			}
		}
		profile := domain.Subscription{Platform: platform, URL: profileURL}
		if err := s.SaveSubscription(&profile); err != nil {
			t.Fatal(err)
		}
		profile.StableID = "author-id"
		if err := s.SaveSubscription(&profile); err != nil {
			t.Fatal(err)
		}
		duplicate := domain.Subscription{Platform: platform, URL: urls[0], StableID: "author-id"}
		if err := s.SaveSubscription(&duplicate); err == nil {
			t.Fatal("重复详情地址被接受")
		}
	}
}
