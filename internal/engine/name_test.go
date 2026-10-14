package engine

import (
	"context"
	"io"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// metadataTransport 模拟官方返回，避免测试依赖平台网络或真实 Cookie。
type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestYouTubeAuthorName 验证视频作者取自元数据而非视频 ID 或标题。
func TestYouTubeAuthorName(t *testing.T) {
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	http.DefaultClient = &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("url") != "https://www.youtube.com/watch?v=video123" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"author_name":"作者频道"}`)), Header: make(http.Header)}, nil
	})}
	name, err := (CLI{}).ResolveName(context.Background(), domain.Subscription{Platform: "youtube", URL: "https://www.youtube.com/watch?v=video123"})
	if err != nil || name != "作者频道" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if youtubeAuthor(youtubeEntry{Title: "播放列表标题"}) != "" {
		t.Fatal("列表标题不是作者")
	}
	if youtubeAuthor(youtubeEntry{Entries: []youtubeEntry{{Uploader: "列表作者"}}}) != "列表作者" {
		t.Fatal("首项作者未解析")
	}
}

// TestInstagramAuthorName 验证网页请求不发送已配置的 Cookie，使用浏览器请求头并返回帖子作者。
func TestInstagramAuthorName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cookies"), 0755); err != nil {
		t.Fatal(err)
	}
	cookie := "# Netscape HTTP Cookie File\n#HttpOnly_.instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\ttest-session\n.other.test\tTRUE\t/\tFALSE\t0\tother\tnot-instagram\n"
	if err := os.WriteFile(filepath.Join(dir, "cookies", "instagram.txt"), []byte(cookie), 0600); err != nil {
		t.Fatal(err)
	}
	original := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = original })
	t.Setenv("INSTAGRAM_USER_AGENT", "test-browser")
	http.DefaultClient = &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("User-Agent") != "test-browser" || r.Header.Get("Accept-Language") != "zh-CN,zh;q=0.9" || r.Header.Get("Referer") != "https://www.instagram.com/" {
			t.Fatal("网页请求头未加载")
		}
		if r.Header.Get("Cookie") != "" {
			t.Fatal("作者解析不应发送 Cookie")
		}
		if r.Header.Get("Sec-Fetch-Dest") != "document" {
			t.Fatal("网页请求类型错误")
		}
		body := `<script type="application/json">{"viewer":{"username":"logged_in_user"},"media":{"code":"DeHR060yl2F","owner":{"username":"yen_210"}}}</script>`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	for address, want := range map[string]string{"https://www.instagram.com/alice/": "alice", "https://www.instagram.com/p/DeHR060yl2F/": "yen_210", "https://www.instagram.com/reel/DeHR060yl2F/": "yen_210"} {
		name, err := (CLI{DataDir: dir}).ResolveName(context.Background(), domain.Subscription{Platform: "instagram", URL: address})
		if err != nil || name != want {
			t.Fatalf("name=%q err=%v", name, err)
		}
	}
}

// TestAutomaticCookies 验证平台配置自动生效、平台隔离及删除配置后的立即停用。
func TestAutomaticCookies(t *testing.T) {
	root := t.TempDir()
	cli := CLI{DataDir: root}
	s := domain.Subscription{Platform: "instagram"}
	if strings.Contains(strings.Join(cli.common(s), " "), "--cookies") {
		t.Fatal("未配置时不应传递 Cookie")
	}
	if err := os.MkdirAll(filepath.Join(root, "cookies"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli.CookiePath("instagram"), []byte("# Netscape HTTP Cookie File\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cli.common(s), " "), "--cookies "+cli.CookiePath("instagram")) {
		t.Fatal("已配置 Cookie 未自动加载")
	}
	if strings.Contains(strings.Join(cli.common(domain.Subscription{Platform: "youtube"}), " "), "--cookies") {
		t.Fatal("Cookie 不能跨平台使用")
	}
	if err := os.Remove(cli.CookiePath("instagram")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cli.common(s), " "), "--cookies") {
		t.Fatal("删除后不应继续加载 Cookie")
	}
}

// TestInstagramRequestHeaders 确认作者解析与下载共用平台级参数，允许环境覆盖且不影响 YouTube。
func TestInstagramRequestHeaders(t *testing.T) {
	cli := CLI{DataDir: t.TempDir()}
	t.Setenv("INSTAGRAM_USER_AGENT", "")
	args := strings.Join(cli.common(domain.Subscription{Platform: "instagram"}), " ")
	for _, value := range []string{"extractor.instagram.user-agent=" + config.InstagramUserAgent, "extractor.instagram.headers=" + config.InstagramHeaders} {
		if !strings.Contains(args, value) {
			t.Fatalf("missing request option: %s", value)
		}
	}
	t.Setenv("INSTAGRAM_USER_AGENT", "test-browser")
	args = strings.Join(cli.common(domain.Subscription{Platform: "instagram"}), " ")
	if !strings.Contains(args, "extractor.instagram.user-agent=test-browser") {
		t.Fatal("UA override not applied")
	}
	if strings.Contains(strings.Join(cli.common(domain.Subscription{Platform: "youtube"}), " "), "extractor.instagram") {
		t.Fatal("Instagram headers leaked into YouTube")
	}
}

// TestResolveAvatarAuthor 验证创建只解析一条头像元数据，稳定身份与媒体列表分离。
func TestResolveAvatarAuthor(t *testing.T) {
	root := t.TempDir()
	script := `#!/bin/sh
case "$*" in
 *--dump-json*--range\ 1*https://www.instagram.com/alice/avatar/*) ;;
 *) exit 1 ;;
esac
printf '%s' '[[3,"https://example.test/avatar.jpg",{"owner_id":"123","username":"alice","post_id":"avatar","extension":"jpg"}]]'
`
	if err := os.WriteFile(filepath.Join(root, "python3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	author, err := (CLI{DataDir: root}).ResolveAuthor(context.Background(), domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/alice/"})
	if err != nil || author.StableID != "123" || author.CurrentName != "alice" || len(author.Items) != 0 {
		t.Fatal(author, err)
	}
}
