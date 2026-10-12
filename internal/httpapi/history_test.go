package httpapi

import (
	"encoding/json"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestHistoryClearAPI 历史和最近下载共用清空语义，任务统计与下载索引保留。
func TestHistoryClearAPI(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sub := domain.Subscription{Platform: "youtube", URL: "https://example.test/author"}
	if err := db.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	if err := db.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: root, ArchiveDir: root}, db, engine.CLI{DataDir: root})
	handler := Handler(app)
	for i := range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("DELETE", "/api/contents/history", nil))
		var result struct {
			Cleared int `json:"cleared"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != 200 || result.Cleared != 1-i {
			t.Fatal(response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/contents", nil))
	if response.Code != 200 || response.Body.String() != "[]\n" {
		t.Fatal(response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/contents/page?page=1", nil))
	var historyPage struct {
		Items []domain.Content `json:"items"`
		Total int              `json:"total"`
		Page  int              `json:"page"`
		Size  int              `json:"page_size"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &historyPage); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(historyPage.Items) != 0 || historyPage.Total != 0 || historyPage.Page != 1 || historyPage.Size < 1 {
		t.Fatal(response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/dashboard", nil))
	var dashboard struct {
		Recent []domain.Content `json:"recent"`
		Counts map[string]int   `json:"counts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(dashboard.Recent) != 0 || dashboard.Counts["queued"] != 1 {
		t.Fatal(dashboard)
	}
	items, _ := db.Contents()
	tasks, _ := db.Tasks()
	if len(items) != 1 || len(tasks) != 1 {
		t.Fatal(items, tasks)
	}
}
