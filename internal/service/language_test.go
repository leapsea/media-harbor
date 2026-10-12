package service

import (
	"context"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

// titleEngine 模拟已有视频复用，只刷新下载元数据。
type titleEngine struct {
	fakeEngine
	language chan string
}

func (e titleEngine) Download(ctx context.Context, s domain.Subscription, item domain.Content, options domain.YouTubeOptions, dir string, log func(string)) error {
	e.language <- s.RequestLanguage
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".metadata.json"), []byte(`{"title":"中文标题"}`), 0644)
}

// TestHistoryTitleUsesCurrentLanguage 已复用文件时，历史仍更新为当前语言的真实元数据标题。
func TestHistoryTitleUsesCurrentLanguage(t *testing.T) {
	e := titleEngine{language: make(chan string, 1)}
	a := testApp(t, e)
	s := addSub(t, a, "language", "youtube")
	settings, _ := a.Repo.Settings()
	settings.UI.Language = "zh-CN"
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Config.DataDir, "existing.mp4")
	if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: "video", Title: "English title", Paths: []string{path}}); err != nil {
		t.Fatal(err)
	}
	items, _ := a.Repo.Contents()
	items[0].Paths = []string{path}
	if err := a.Repo.SaveContent(&items[0]); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	if language := <-e.language; language != "zh-CN" {
		t.Fatal(language)
	}
	items, _ = a.Repo.Contents()
	if len(items) != 1 || items[0].Title != "中文标题" || len(items[0].Paths) != 1 || items[0].Paths[0] != path {
		t.Fatal(items)
	}
}
