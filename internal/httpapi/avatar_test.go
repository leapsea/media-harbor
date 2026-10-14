package httpapi

import (
	"context"
	"encoding/json"
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
)

// TestSharedAuthorAvatar 同一作者的主页与详情通过各自任务地址读取同一个本地头像。
func TestSharedAuthorAvatar(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, address := range []string{"https://www.instagram.com/alice/", "https://www.instagram.com/p/post/"} {
		sub := domain.Subscription{Platform: "instagram", URL: address, StableID: "123", CurrentName: "alice"}
		if err = db.SaveSubscription(&sub); err != nil {
			t.Fatal(err)
		}
		path := engine.AvatarPath(filepath.Join(root, "downloads"), sub)
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("avatar-fixture"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	app := service.New(config.Config{DataDir: root, DownloadDir: filepath.Join(root, "downloads"), ArchiveDir: filepath.Join(root, "archive")}, db, engine.CLI{DataDir: root})
	handler := Handler(app)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/subscriptions", nil))
	var views []service.SubscriptionView
	if err = json.Unmarshal(response.Body.Bytes(), &views); err != nil || len(views) != 2 {
		t.Fatal(response.Body.String(), err)
	}
	for _, view := range views {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", view.AvatarURL, nil))
		if response.Code != 200 || response.Body.String() != "avatar-fixture" {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/subscriptions/999/avatar", nil))
	if response.Code != 404 {
		t.Fatal(response.Code)
	}
}

// creationAvatarEngine 将创建请求的作者解析与头像下载限定为本地确定性结果。
type creationAvatarEngine struct {
	engine.CLI
	calls int
}

func (e *creationAvatarEngine) ResolveAuthor(context.Context, domain.Subscription) (domain.ScanResult, error) {
	return domain.ScanResult{StableID: "123", CurrentName: "alice"}, nil
}
func (e *creationAvatarEngine) UpdateAvatar(_ context.Context, _ domain.Subscription, path string, _ func(string)) error {
	e.calls++
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("avatar-fixture"), 0644)
}

// TestCreateAvatarBeforeMedia 验证创建 API 返回前头像可读、主页未启动媒体任务、编辑不重复下载。
func TestCreateAvatarBeforeMedia(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	eng := &creationAvatarEngine{}
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: filepath.Join(root, "downloads"), ArchiveDir: filepath.Join(root, "archive")}, db, eng)
	// 离线头像测试满足Instagram登录前置条件，不访问真实账号。
	if err := app.Credentials.Save("instagram", "# Netscape HTTP Cookie File\n.instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\toffline-test\n"); err != nil {
		t.Fatal(err)
	}
	handler := Handler(app)
	body := `{"platform":"instagram","url":"https://www.instagram.com/alice/","display_name":"custom","enabled":false,"archive":false}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/api/subscriptions", strings.NewReader(body)))
	var sub domain.Subscription
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &sub) != nil || sub.StableID != "123" || sub.CurrentName != "alice" {
		t.Fatal(response.Code, response.Body.String())
	}
	tasks, _ := db.Tasks()
	if len(tasks) != 0 {
		t.Fatal(tasks)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/subscriptions/1/avatar", nil))
	if response.Code != 200 || response.Body.String() != "avatar-fixture" {
		t.Fatal(response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("PUT", "/api/subscriptions/1", strings.NewReader(body)))
	if response.Code != 200 || eng.calls != 1 {
		t.Fatal(response.Code, response.Body.String(), eng.calls)
	}
}

// 启动迁移 downloads/归档中的旧头像，接口继续同源提供正确图片类型。
func TestLegacyAvatarMigrationServesImageType(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sub := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/legacy/", StableID: "legacy", CurrentName: "legacy"}
	if err = db.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	// 两个挂载目录分别迁移，旧 data 目录不参与读取。
	cfg := config.Config{DataDir: root, DownloadDir: filepath.Join(root, "downloads"), ArchiveDir: filepath.Join(root, "archive")}
	body := []byte("GIF89a" + strings.Repeat("x", 32))
	for _, dir := range []string{cfg.DownloadDir, cfg.ArchiveDir} {
		path := strings.TrimSuffix(engine.AvatarPath(dir, sub), ".jpg") + ".img"
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, body, 0644); err != nil {
			t.Fatal(err)
		}
	}
	app := service.New(cfg, db, engine.CLI{DataDir: root})
	response := httptest.NewRecorder()
	Handler(app).ServeHTTP(response, httptest.NewRequest("GET", "/api/subscriptions/1/avatar", nil))
	if response.Code != 200 || response.Header().Get("Content-Type") != "image/gif" || response.Body.String() != string(body) {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
	for _, dir := range []string{cfg.DownloadDir, cfg.ArchiveDir} {
		path := engine.AvatarPath(dir, sub)
		if filepath.Ext(path) != ".gif" {
			t.Fatal(path)
		}
		old := strings.TrimSuffix(path, ".gif") + ".img"
		if _, err = os.Stat(old); !os.IsNotExist(err) {
			t.Fatal("旧img仍在", err)
		}
	}
}
