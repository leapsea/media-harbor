package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dryEngine 只允许请求验证，普通扫描或下载被调用即说明错误复用了落盘流程。
type dryEngine struct {
	fail    bool
	limit   int
	options domain.YouTubeOptions
}

func (e *dryEngine) Scan(context.Context, domain.Subscription, int, func(string), func(domain.ScanResult) error) (domain.ScanResult, error) {
	panic("dryrun 不应扫描入队")
}
func (e *dryEngine) Download(context.Context, domain.Subscription, domain.Content, domain.YouTubeOptions, string, func(string)) error {
	panic("dryrun 不应调用落盘下载")
}
func (e *dryEngine) DryRun(_ context.Context, _ domain.Subscription, o domain.YouTubeOptions, limit int, log func(string)) error {
	e.limit = limit
	e.options = o
	log("请求阶段：dry-fixture；HTTP 200")
	if e.fail {
		return fmt.Errorf("fixture HTTP 429")
	}
	return nil
}

// TestDryRunStreamWithoutPersistence 流式日志、失败结果和自定义选项均不创建配置或任务。
func TestDryRunStreamWithoutPersistence(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	eng := &dryEngine{}
	app := service.New(config.Config{DataDir: root, LogDir: filepath.Join(root, "logs"), DownloadDir: filepath.Join(root, "downloads"), ArchiveDir: filepath.Join(root, "archive")}, db, eng)
	saved, _ := app.Settings()
	saved.Debug.TestItemLimit = 10
	if err = app.SaveSettings(saved); err != nil {
		t.Fatal(err)
	}
	handler := Handler(app)
	present := map[string]bool{}
	for _, path := range []string{"logs", "downloads", "archive"} {
		_, err := os.Stat(filepath.Join(root, path))
		present[path] = err == nil
	}
	for _, fail := range []bool{false, true} {
		eng.fail = fail
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/subscriptions/dryrun", strings.NewReader(`{"platform":"youtube","url":"https://example.test/video","youtube":{"subtitle_languages":"zh-CN","auto_subtitles":true}}`)))
		if !strings.Contains(response.Body.String(), "请求阶段") || !strings.Contains(response.Header().Get("Content-Type"), "ndjson") {
			t.Fatal(response.Body.String())
		}
		lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
		var result struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err = json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil || result.OK == fail {
			t.Fatal(result, err)
		}
		if eng.limit != 10 || eng.options.SubtitleLanguages != "zh-CN" {
			t.Fatal(eng)
		}
	}
	subs, _ := db.Subscriptions()
	tasks, _ := db.Tasks()
	contents, _ := db.Contents()
	if len(subs)+len(tasks)+len(contents) != 0 {
		t.Fatal("dryrun 产生了数据库记录")
	}
	for _, path := range []string{"logs", "downloads", "archive"} {
		entries, err := os.ReadDir(filepath.Join(root, path))
		if (err == nil) != present[path] || len(entries) != 0 {
			t.Fatal("dryrun 改变了目录或新增了文件", path, err)
		}
	}
}
