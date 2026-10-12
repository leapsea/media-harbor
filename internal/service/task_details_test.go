package service

import (
	"encoding/json"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 持久进度在取消后不能改回运行，失败保留最后的实际字节数。
func TestProgressLifecycle(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "progress", "youtube")
	if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "video"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	task.Status = "running"
	_ = a.Repo.SaveTask(&task)
	record := func(p domain.TransferProgress) {
		raw, _ := json.Marshal(p)
		if !a.recordProgress(task.ID, engine.ProgressPrefix+string(raw)) {
			t.Fatal("event not consumed")
		}
	}
	record(domain.TransferProgress{Kind: "video", Status: "running", Title: "本地标题", BytesTotal: 10 * 1024 * 1024, BytesDone: 5 * 1024 * 1024, Percent: 50})
	record(domain.TransferProgress{Kind: "video", Status: "failed", Error: "HTTP 403"})
	task, _ = a.Repo.Task(task.ID)
	if len(task.Progress) != 1 || task.Progress[0].BytesDone != 5*1024*1024 || task.Progress[0].Title != "本地标题" {
		t.Fatalf("%+v", task.Progress)
	}
	task.Status = "cancelled"
	_ = a.Repo.SaveTask(&task)
	record(domain.TransferProgress{Kind: "video", Status: "completed", Percent: 100})
	task, _ = a.Repo.Task(task.ID)
	if task.Status != "cancelled" || task.Progress[0].Status != "failed" {
		t.Fatalf("%+v", task)
	}
}

// 一条视频对应各附属项；元数据和评论关联同一产物，完成状态由真实文件证明。
func TestYouTubeTaskDetails(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "details", "youtube")
	s.YouTube = &domain.YouTubeOptions{Video: true, ExtraAudio: true, Metadata: true, Comments: true, Subtitles: true}
	_ = a.Repo.SaveSubscription(&s)
	_ = a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "video", Title: "标题"})
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	content, _ := a.Repo.Content(task.ContentID)
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	file, err := os.Create(video)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Truncate(6 * 1024 * 1024)
	_ = file.Close()
	meta := filepath.Join(root, "video.info.json")
	_ = os.WriteFile(meta, []byte(`{"comments":[]}`), 0644)
	content.Paths = []string{video, meta}
	_ = a.Repo.SaveContent(&content)
	task.Status = "completed"
	_ = a.Repo.SaveTask(&task)
	detail, err := a.TaskDetails(s.ID)
	if err != nil || len(detail.Items) != 1 {
		t.Fatalf("%+v %v", detail, err)
	}
	parts := detail.Items[0].Parts
	if len(parts) != 4 || parts[0].BytesTotal != 6*1024*1024 || parts[0].Status != "completed" || len(parts[1].Labels) != 2 || parts[2].Status != "skipped" || parts[3].Status != "skipped" {
		t.Fatalf("%+v", parts)
	}
}

// 轮播按帖子归组，照片与视频各自编号，不把三个媒体误认为三个帖子。
func TestInstagramTaskDetails(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "carousel", "instagram")
	for i, kind := range []string{"photo", "photo", "video"} {
		_ = a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: []string{"post-1", "post-2", "post-3"}[i], Asset: i + 1, Title: "轮播", MediaType: kind})
	}
	tasks, _ := a.Repo.Tasks()
	tasks[0].Status = "completed"
	_ = a.Repo.SaveTask(&tasks[0])
	tasks[1].Status = "running"
	tasks[1].Progress = []domain.TransferProgress{{Kind: "media", Status: "running", BytesTotal: 8 * 1024 * 1024, Percent: 40}}
	_ = a.Repo.SaveTask(&tasks[1])
	detail, err := a.TaskDetails(s.ID)
	if err != nil || len(detail.Items) != 1 {
		t.Fatalf("%+v %v", detail, err)
	}
	item := detail.Items[0]
	if item.Status != "running" || len(item.Parts) != 3 || item.Parts[0].Number != 1 || item.Parts[1].Number != 2 || item.Parts[2].Number != 1 || item.Parts[2].Labels[0] != "视频" {
		t.Fatalf("%+v", item)
	}
}

func TestConciseTaskLog(t *testing.T) {
	raw := "[2026-10-12 01:00:00] 开始 download 任务 #1；第 1 次执行\n[2026-10-12 01:00:01] ERROR: old failure\n[2026-10-12 02:00:00] 开始 download 任务 #1；第 2 次执行\n[2026-10-12 02:00:01] [youtube] Downloading webpage\n[2026-10-12 02:00:02] [download] 40% of 1MiB ETA 00:01\n[2026-10-12 02:00:03] 主任务：开始下载视频\n[2026-10-12 02:00:04] WARNING: media denied\n[2026-10-12 02:00:05] ERROR: HTTP 403\n[2026-10-12 02:00:06] 本次执行结束，耗时 6s\n"
	got := conciseTaskLog(raw)
	for _, noise := range []string{"old failure", "Downloading webpage", "40%"} {
		if strings.Contains(got, noise) {
			t.Fatal(got)
		}
	}
	for _, want := range []string{"02:00:03] 主任务", "WARNING: media denied", "ERROR: HTTP 403", "耗时 6s"} {
		if !strings.Contains(got, want) {
			t.Fatal(got)
		}
	}
}

// 已结束项在上方，重试后的新执行按开始时间排在运行区下方，等待项不遮住运行内容。
func TestTaskDetailsExecutionOrder(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "order", "youtube")
	for _, id := range []string{"retry", "older", "waiting"} {
		_ = a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: id})
	}
	tasks, _ := a.Repo.Tasks()
	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for _, task := range tasks {
		content, _ := a.Repo.Content(task.ContentID)
		task.CreatedAt = base
		switch content.RemoteID {
		case "retry":
			task.Status = "running"
			task.StartedAt = base.Add(4 * time.Minute)
		case "older":
			task.Status = "completed"
			task.StartedAt = base.Add(2 * time.Minute)
		case "waiting":
			task.Status = "queued"
			task.CreatedAt = base.Add(3 * time.Minute)
		}
		_ = a.Repo.SaveTask(&task)
	}
	detail, err := a.TaskDetails(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Items) != 3 || detail.Items[0].Key != "older" || detail.Items[1].Key != "retry" || detail.Items[2].Key != "waiting" {
		t.Fatalf("%+v", detail)
	}
}
