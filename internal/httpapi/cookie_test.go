package httpapi

import (
	"encoding/json"
	"media-harbor/internal/config"
	"media-harbor/internal/engine"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCookieTextEditor 验证编辑文本的空值读取、保存、重开及删除。
// 使用无效测试凭据，不连接真实平台；换行与 tab 必须原样保存。
func TestCookieTextEditor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cookies"), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(root, "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := service.New(config.Config{DataDir: root, LogDir: root, DownloadDir: root, ArchiveDir: root}, db, engine.CLI{DataDir: root})
	handler := Handler(app)
	// 常规状态与编辑内容分离，打开未配置的平台应显示空文本。
	request := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}
	initial := request(http.MethodGet, "/api/cookies/instagram/content", "")
	if initial.Code != 200 || !strings.Contains(initial.Body.String(), `"content":""`) {
		t.Fatal(initial.Body.String())
	}
	content := "# Netscape HTTP Cookie File\n.example.test\tTRUE\t/\tFALSE\t0\ttest\tplaceholder\n"
	payload, _ := json.Marshal(map[string]string{"content": content})
	saved := request(http.MethodPut, "/api/cookies/instagram", string(payload))
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	// 文件仍采用引擎读取的标准格式，编辑 API 不加入 JSON 包装。
	raw, err := os.ReadFile(filepath.Join(root, "cookies", "instagram.txt"))
	if err != nil || string(raw) != content {
		t.Fatalf("cookie persistence: %v", err)
	}
	reopened := request(http.MethodGet, "/api/cookies/instagram/content", "")
	var response struct{ Content string }
	if err = json.Unmarshal(reopened.Body.Bytes(), &response); err != nil || response.Content != content {
		t.Fatal("cookie reload mismatch")
	}
	status := request(http.MethodGet, "/api/cookies/instagram", "")
	if !strings.Contains(status.Body.String(), `"configured":true`) {
		t.Fatal(status.Body.String())
	}
	// 浏览器 Cookie 请求头经保存接口转成七列文件，百分号编码和值内等号保持不变。
	payload, _ = json.Marshal(map[string]string{"content": "sessionid=test%3Avalue; csrftoken=abc=def"})
	converted := request(http.MethodPut, "/api/cookies/instagram", string(payload))
	raw, err = os.ReadFile(filepath.Join(root, "cookies", "instagram.txt"))
	if converted.Code != 200 || err != nil || !strings.Contains(string(raw), ".instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\ttest%3Avalue\n") {
		t.Fatal("请求头未转换为引擎格式")
	}
	deleted := request(http.MethodDelete, "/api/cookies/instagram", "")
	if deleted.Code != 200 {
		t.Fatal(deleted.Body.String())
	}
	// 删除后编辑器回到空值，未配置状态没有残留文本。
	empty := request(http.MethodGet, "/api/cookies/instagram/content", "")
	if !strings.Contains(empty.Body.String(), `"content":""`) {
		t.Fatal(empty.Body.String())
	}
}
