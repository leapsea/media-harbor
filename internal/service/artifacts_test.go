package service

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestArtifactTags 区分真实字幕、评论与普通元数据，空评论列表也是已保存的评论结果。
func TestArtifactTags(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"video.mp4", "", []string{"video"}},
		{"audio.m4a", "", []string{"audio"}},
		{"video.zh.vtt", "", []string{"subtitles"}},
		{"metadata.info.json", `{ "title": "video" }`, []string{"metadata"}},
		{"comments.info.json", `{ "comments": [] }`, []string{"metadata", "comments"}},
		{"missing.info.json", `{ "comments": null }`, []string{"metadata"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			if got := fileTags(path, "youtube"); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestDirectoryDateSource 验证两种日期来源、元数据补读和缺失日期，日期开关不修改真实发布时间。
func TestDirectoryDateSource(t *testing.T) {
	previousLocation := time.Local
	time.Local = time.FixedZone("local-test", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocation })
	for _, tc := range []struct {
		name                      string
		enabled                   bool
		published, metadata, want string
	}{
		{"upload", true, "2020-01-02", "", "2020/0102"},
		{"task", false, "2020-01-02", "", "2026/1012"},
		{"metadata", true, "", `{"upload_date":"20190304"}`, "2019/0304"},
		{"creation fallback", true, "", "", "2026/1012"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t, fakeEngine{})
			settings, _ := a.Repo.Settings()
			settings.UseUploadDate = tc.enabled
			if err := a.Repo.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			s := addSub(t, a, "author", "youtube")
			s.CreatedAt = time.Date(2026, 10, 11, 18, 30, 0, 0, time.UTC)
			c := domain.Content{RemoteID: "one"}
			if tc.published != "" {
				c.Published, _ = time.Parse("2006-01-02", tc.published)
			}
			if err := a.Repo.AddContentAndTask(s.ID, c); err != nil {
				t.Fatal(err)
			}
			items, _ := a.Repo.Contents()
			c = items[0]
			work := filepath.Join(t.TempDir(), "work")
			os.MkdirAll(work, 0755)
			os.WriteFile(filepath.Join(work, "one.mp4"), []byte("media"), 0644)
			if tc.metadata != "" {
				os.WriteFile(filepath.Join(work, ".metadata.json"), []byte(tc.metadata), 0644)
			}
			if err := a.placeFiles(s, &c, work); err != nil {
				t.Fatal(err)
			}
			if len(c.Paths) != 1 || !strings.Contains(c.Paths[0], tc.want) || !reflect.DeepEqual(c.Tags, []string{"video"}) {
				t.Fatal(c)
			}
			if tc.published != "" && c.Published.Format("2006-01-02") != tc.published {
				t.Fatal("发布时间被目录时间覆盖")
			}
		})
	}
}

// TestContentHistoryArtifactTags 下载历史从实际评论文件获得标签，读取历史不重写下载状态。
func TestContentHistoryArtifactTags(t *testing.T) {
	a := testApp(t, fakeEngine{})
	s := addSub(t, a, "author", "youtube")
	path := filepath.Join(t.TempDir(), "old.info.json")
	if err := os.WriteFile(path, []byte(`{"comments":[{"text":"hello"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "old"}); err != nil {
		t.Fatal(err)
	}
	items, _ := a.Repo.Contents()
	c := items[0]
	c.Paths = []string{path}
	c.Status = "completed"
	if err := a.Repo.SaveContent(&c); err != nil {
		t.Fatal(err)
	}
	history, err := a.ContentHistory()
	if err != nil || !reflect.DeepEqual(history[0].Tags, []string{"metadata", "comments"}) {
		t.Fatal(history, err)
	}
	if len(history[0].Files) != 1 || history[0].Files[0].Path != path || !reflect.DeepEqual(history[0].Files[0].Tags, []string{"metadata", "comments"}) || history[0].Files[0].CommentCount == nil || *history[0].Files[0].CommentCount != 1 {
		t.Fatal("文件标签关联错误", history[0].Files)
	}
	saved, _ := a.Repo.Content(c.ID)
	if saved.Status != "completed" || len(saved.Tags) != 0 {
		t.Fatal("历史读取不应覆盖内容快照", saved)
	}
}

// TestCommentCount 区分实际评论数量、零条结果与未下载评论。
func TestCommentCount(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
		hasCount   bool
	}{
		{"saved", `{"comments":[{"text":"one"},{"text":"two"}],"comment_count":99}`, 2, true},
		{"empty", `{"comments":[]}`, 0, true},
		{"missing", `{"title":"video"}`, 0, false},
		{"null", `{"comments":null}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "video.info.json")
			if err := os.WriteFile(path, []byte(tc.body), 0644); err != nil {
				t.Fatal(err)
			}
			file := infoFileDetails(path)
			if (file.CommentCount != nil) != tc.hasCount {
				t.Fatal(file)
			}
			if tc.hasCount && *file.CommentCount != tc.want {
				t.Fatal(file)
			}
		})
	}
}

// TestDirectoryUploadLocalTime 验证上传时刻跨越本地日期边界时使用本地日期。
func TestDirectoryUploadLocalTime(t *testing.T) {
	previousLocation := time.Local
	time.Local = time.FixedZone("local-test", 8*60*60)
	t.Cleanup(func() { time.Local = previousLocation })
	date := time.Date(2026, 10, 11, 18, 30, 0, 0, time.UTC)
	if got := renderPathAt("{year}/{month}{day}/{filename}", domain.Subscription{}, domain.Content{}, "video.mp4", date); got != "2026/1012/video.mp4" {
		t.Fatal(got)
	}
}

// partialArtifactEngine 模拟音频和元数据已经完成，但视频阶段失败。
type partialArtifactEngine struct {
	fakeEngine
	fail bool
}

func (e partialArtifactEngine) Download(_ context.Context, _ domain.Subscription, _ domain.Content, _ domain.YouTubeOptions, dir string, _ func(string)) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	metadata := `{"comments":[{"text":"first"}]}`
	if e.fail {
		if err := os.WriteFile(filepath.Join(dir, "id.audio.m4a"), []byte("original-audio"), 0644); err != nil {
			return err
		}
	} else {
		metadata = `{"comments":[{"text":"first"},{"text":"new"}]}`
		if err := os.WriteFile(filepath.Join(dir, "id.mp4"), []byte("video"), 0644); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "id.info.json"), []byte(metadata), 0644); err != nil {
		return err
	}
	if e.fail {
		return errors.New("video download failed")
	}
	return nil
}

// TestPartialArtifactsSurviveRetry 已完成音频入索引，重试补视频并原位覆盖评论元数据。
func TestPartialArtifactsSurviveRetry(t *testing.T) {
	a := testApp(t, partialArtifactEngine{fail: true})
	sub := addSub(t, a, "author", "youtube")
	if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "id", URL: "https://example.com/id"}); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	items, _ := a.Repo.Contents()
	item := items[0]
	if item.Status != "failed" || len(item.Paths) != 2 {
		t.Fatalf("partial=%+v", item)
	}
	var audio, metadata string
	for _, p := range item.Paths {
		if strings.HasSuffix(p, ".m4a") {
			audio = p
		}
		if strings.HasSuffix(p, ".info.json") {
			metadata = p
		}
	}
	before, _ := os.Stat(audio)
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	a.Engine = partialArtifactEngine{}
	if err := a.RetryTask(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	item, _ = a.Repo.Content(item.ID)
	if item.Status != "completed" || len(item.Paths) != 3 {
		t.Fatalf("completed=%+v", item)
	}
	after, _ := os.Stat(audio)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("复用音频被改写")
	}
	if got := infoFileDetails(metadata).CommentCount; got == nil || *got != 2 {
		t.Fatal("评论没有原位更新", got)
	}
	if _, err := os.Stat(a.WorkPath(task)); !os.IsNotExist(err) {
		t.Fatal("完成后临时目录未清理", err)
	}
}

// TestArchivedMetadataRefresh 元数据直接更新既有归档位置，已归档音频不搬回下载目录。
func TestArchivedMetadataRefresh(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "author", "youtube")
	_ = a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "id"})
	items, _ := a.Repo.Contents()
	item := items[0]
	dir := filepath.Join(a.Config.ArchiveDir, "youtube", "author")
	_ = os.MkdirAll(dir, 0755)
	audio := filepath.Join(dir, "id.audio.m4a")
	metadata := filepath.Join(dir, "id.info.json")
	_ = os.WriteFile(audio, []byte("audio"), 0644)
	_ = os.WriteFile(metadata, []byte(`{"comments":[]}`), 0644)
	item.Paths = []string{audio, metadata}
	item.Archived = true
	_ = a.Repo.SaveContent(&item)
	work := t.TempDir()
	_ = os.WriteFile(filepath.Join(work, "changed-title.info.json"), []byte(`{"comments":[{}]}`), 0644)
	if err := a.placeFiles(sub, &item, work); err != nil {
		t.Fatal(err)
	}
	if len(item.Paths) != 2 || !item.Archived || *infoFileDetails(metadata).CommentCount != 1 {
		t.Fatal(item)
	}
	if _, err := os.Stat(audio); err != nil {
		t.Fatal(err)
	}
}

// TestCatchupRefreshesCompletedMetadata 已完成内容的补全仍刷新元数据，但复用原索引与下载任务。
func TestCatchupRefreshesCompletedMetadata(t *testing.T) {
	a := testApp(t, fakeEngine{})
	sub := addSub(t, a, "author", "youtube")
	_ = a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "id", URL: "https://example.com/id"})
	items, _ := a.Repo.Contents()
	item := items[0]
	audio := filepath.Join(t.TempDir(), "id.audio.m4a")
	_ = os.WriteFile(audio, []byte("audio"), 0644)
	item.Paths = []string{audio}
	item.Status = "completed"
	_ = a.Repo.SaveContent(&item)
	tasks, _ := a.Repo.Tasks()
	task := tasks[0]
	task.Status = "completed"
	task.Attempts = 1
	_ = a.Repo.SaveTask(&task)
	added, retried, completed, _, _, err := a.reconcileScanLocked(sub.ID, []domain.Content{item}, true)
	if err != nil || added != 0 || retried != 1 || completed != 0 {
		t.Fatal(added, retried, completed, err)
	}
	after, _ := a.Repo.Tasks()
	saved, _ := a.Repo.Content(item.ID)
	if len(after) != 1 || after[0].ID != task.ID || after[0].Status != "queued" || len(saved.Paths) != 1 || saved.Paths[0] != audio {
		t.Fatal(after, saved)
	}
}
