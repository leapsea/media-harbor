package httpapi

import (
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestRecentDownloadLimit 总览可突破旧的 8 条限制，展示数量不减少真实内容记录。
func TestRecentDownloadLimit(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := domain.Subscription{Platform: "youtube", URL: "https://example.test/author"}
	if err = db.SaveSubscription(&s); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		if err = db.AddContentAndTask(s.ID, domain.Content{RemoteID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: root, ArchiveDir: root}, db, engine.CLI{DataDir: root})
	handler := Handler(app)
	for _, count := range []int{5, 10, 20} {
		settings, _ := db.Settings()
		settings.UI.RecentDownloads = count
		if err = app.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/dashboard", nil))
		var result struct {
			Recent []domain.Content `json:"recent"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || len(result.Recent) != min(count, 12) {
			t.Fatal(count, response.Body.String())
		}
	}
	items, _ := db.Contents()
	if len(items) != 12 {
		t.Fatal("展示数量不应改变下载范围")
	}
}

// TestInterfaceLanguage 验证五种语言可保存，未知语言不能进入界面配置。
func TestInterfaceLanguage(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: root, ArchiveDir: root}, db, engine.CLI{DataDir: root})
	for _, language := range []string{"zh-CN", "en", "hi", "ru", "ja"} {
		settings, _ := db.Settings()
		settings.UI.Language = language
		if err = app.SaveSettings(settings); err != nil {
			t.Fatal(language, err)
		}
		saved, err := db.Settings()
		if err != nil || saved.UI.Language != language {
			t.Fatal(saved, err)
		}
	}
	settings, _ := db.Settings()
	settings.UI.Language = "unsupported"
	if err = app.SaveSettings(settings); err == nil {
		t.Fatal("不支持的语言被接受")
	}
}

// TestDebugSettings 保存自定义测试限制以及关闭状态，拒绝负数。
func TestDebugSettings(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: root, ArchiveDir: root}, db, engine.CLI{DataDir: root})
	for _, limit := range []int{10, 1, 5, 25, 0} {
		settings, _ := db.Settings()
		settings.Debug.TestItemLimit = limit
		if err := app.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
		saved, err := db.Settings()
		if err != nil || saved.Debug.TestItemLimit != limit {
			t.Fatal(saved, err)
		}
	}
	settings, _ := db.Settings()
	settings.Debug.TestItemLimit = -1
	if err := app.SaveSettings(settings); err == nil {
		t.Fatal("接受了负数测试限制")
	}
}
