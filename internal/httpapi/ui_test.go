package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			Recent            []domain.Content  `json:"recent"`
			Language          string            `json:"language"`
			SubscriptionNames map[string]string `json:"subscription_names"`
			Counts            map[string]int    `json:"counts"`
		}
		if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		_, hasSubscriptionName := result.SubscriptionNames[fmt.Sprint(s.ID)]
		if response.Code != 200 || len(result.Recent) != min(count, 12) || result.Language != settings.UI.Language || !hasSubscriptionName || result.Counts["queued"] != 12 {
			t.Fatal(count, response.Body.String())
		}
		if result.Recent[0].RemoteID != "11" {
			t.Fatal("总览应按最新记录优先返回", result.Recent)
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

// TestTaskLogLineSettings 窗口默认只返回最近 100 行，设置保存后下一次请求立即生效。
func TestTaskLogLineSettings(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: root, ArchiveDir: root}, db, engine.CLI{DataDir: root})
	sub := domain.Subscription{Platform: "youtube", URL: "https://www.youtube.com/@author", DisplayName: "author"}
	if err := app.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&body, "line-%03d\n", i)
	}
	if err := os.WriteFile(app.LogPath(sub, time.Now()), []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	handler := Handler(app)
	for _, limit := range []int{100, 50, 200} {
		if limit != 100 {
			settings, _ := app.Settings()
			settings.UI.TaskLogLines = limit
			raw, _ := json.Marshal(settings)
			saved := httptest.NewRecorder()
			handler.ServeHTTP(saved, httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(raw)))
			if saved.Code != 200 {
				t.Fatal(saved.Code, saved.Body.String())
			}
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", fmt.Sprintf("/api/subscriptions/%d/log", sub.ID), nil))
		var result struct {
			Log string `json:"log"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || strings.Count(result.Log, "\n") != limit || !strings.HasPrefix(result.Log, fmt.Sprintf("line-%03d", 200-limit)) {
			t.Fatal(limit, response.Body.String(), err)
		}
	}
	settings, _ := app.Settings()
	settings.UI.TaskLogLines = -1
	if err := app.SaveSettings(settings); err == nil {
		t.Fatal("接受了负数日志行数")
	}
}
