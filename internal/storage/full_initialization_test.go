package storage

import (
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"path/filepath"
	"reflect"
	"testing"
)

// 真实数据库验证删除范围、文件索引和执行历史，清空详情不是修改展示过滤器。
func TestFullInitializationScopeAndFreshDownloads(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"target", "other"} {
		sub := domain.Subscription{Platform: "youtube", URL: "https://youtube.com/@" + name}
		if err = s.SaveSubscription(&sub); err != nil {
			t.Fatal(err)
		}
		if err = s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.SubscriptionTasks(1)
	old := before[0]
	content, _ := s.Content(old.ContentID)
	content.Paths, content.Status = []string{"/archive/original.mp4"}, "completed"
	if err = s.SaveContent(&content); err != nil {
		t.Fatal(err)
	}
	old.Status = "completed"
	if err = s.SaveTask(&old); err != nil {
		t.Fatal(err)
	}
	otherPosts, _ := s.Posts(2)
	otherItems, _ := s.DownloadItems(otherPosts[0].ID)
	root, err := s.ReinitializeSubscription(1)
	if err != nil {
		t.Fatal(err)
	}
	posts, _ := s.Posts(1)
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM download_items WHERE content_id=?", content.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 0 || count != 0 {
		t.Fatal("详情关系未清空", posts, count)
	}
	retained, _ := s.Content(content.ID)
	if retained.PostID != 0 || !reflect.DeepEqual(retained.Paths, content.Paths) {
		t.Fatal(retained)
	}
	currentOtherPosts, _ := s.Posts(2)
	currentOtherItems, _ := s.DownloadItems(otherPosts[0].ID)
	if !reflect.DeepEqual(otherPosts, currentOtherPosts) || !reflect.DeepEqual(otherItems, currentOtherItems) {
		t.Fatal("误修改其他配置")
	}
	// 最新初始化边界生效，历史下载不占列表统计和调试范围。
	stats, err := s.TaskGroupStats(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range stats {
		if group.SubscriptionID == 1 && (group.Total != 0 || group.ScanStatus != "queued") {
			t.Fatal(group)
		}
	}
	item := domain.Content{RemoteID: "one", Title: "updated", SourceTaskID: root.ID}
	for i := 0; i < 2; i++ {
		queued, err := s.QueueFullContent(1, item)
		if err != nil || queued != (i == 0) {
			t.Fatal(queued, err)
		}
	}
	tasks, _ := s.SubscriptionTasks(1)
	if len(tasks) != 3 || tasks[0].ID <= root.ID || !tasks[0].Overwrite || tasks[0].SourceTaskID != root.ID {
		t.Fatal(tasks)
	}
	// 原任务保留，新执行关联原内容身份且不继承完成结果。
	if tasks[0].ContentID != content.ID {
		t.Fatal(tasks[0])
	}
	posts, _ = s.Posts(1)
	if len(posts) != 1 || posts[0].Status != "queued" || posts[0].Completed != 0 {
		t.Fatal(posts)
	}
	resources, _ := s.DownloadItems(posts[0].ID)
	for _, d := range resources {
		if d.Status != "queued" || len(d.Paths) != 0 {
			t.Fatal(d)
		}
	}
}

// 在最终入队制造唯一约束失败，证明取消、删除及内容关联都回滚。
func TestFullInitializationRollback(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sub := domain.Subscription{Platform: "youtube", URL: "https://youtube.com/@rollback"}
	if err = s.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	if err = s.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enqueue(sub.ID, 0, "full"); err != nil {
		t.Fatal(err)
	}
	posts, _ := s.Posts(sub.ID)
	resources, _ := s.DownloadItems(posts[0].ID)
	tasks, _ := s.SubscriptionTasks(sub.ID)
	contents, _ := s.Contents()
	if _, err = s.db.Exec("CREATE UNIQUE INDEX reject_second_full ON tasks(subscription_id,kind) WHERE kind='full'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReinitializeSubscription(sub.ID); err == nil {
		t.Fatal("预期入队失败")
	}
	newPosts, _ := s.Posts(sub.ID)
	newResources, _ := s.DownloadItems(posts[0].ID)
	newTasks, _ := s.SubscriptionTasks(sub.ID)
	newContents, _ := s.Contents()
	if !reflect.DeepEqual(posts, newPosts) || !reflect.DeepEqual(resources, newResources) || !reflect.DeepEqual(tasks, newTasks) || !reflect.DeepEqual(contents, newContents) {
		t.Fatal("清理事务没有完整回滚")
	}
}
