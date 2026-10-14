package credentials

import (
	"os"
	"strings"
	"testing"
	"time"
)

const instagramFixture = "# Netscape HTTP Cookie File\n.instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\toffline-session\n"
const youtubeFixture = "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\toffline-sid\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\toffline-info\n"

// TestCredentialPlatformPolicy Instagram必须认证，YouTube可匿名；文件存在不等于可用。
func TestCredentialPlatformPolicy(t *testing.T) {
	s := New(t.TempDir())
	for _, p := range []string{"instagram", "youtube"} {
		v, err := s.Status(p)
		if err != nil || v.Configured || v.PlatformEnabled != (p == "youtube") {
			t.Fatal(v, err)
		}
	}
	// 保存格式正确的登录字段后允许请求，但不能宣称已通过远端验证。
	if err := s.Save("instagram", instagramFixture); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Status("instagram")
	if !v.Usable || v.State != "unverified" {
		t.Fatal(v)
	}
	changed, err := s.Disable("instagram", v.Revision, "Instagram 登录会话失效")
	if !changed || err != nil {
		t.Fatal(changed, err)
	}
	// 重建管理器模拟进程重启，停用状态与Cookie原文均保留。
	other := New(strings.TrimSuffix(s.root, "/cookies"))
	v, _ = other.Status("instagram")
	if v.Usable || v.PlatformEnabled || v.State != "disabled" {
		t.Fatal(v)
	}
	// 停用操作只影响可用性，原文仍可通过编辑接口取回。
	raw, _ := os.ReadFile(s.Path("instagram"))
	if string(raw) != instagramFixture {
		t.Fatal("停用不应删改原文")
	}
	if err := s.Save("youtube", youtubeFixture); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Status("youtube")
	_, _ = s.Disable("youtube", v.Revision, "YouTube 登录 Cookie 已失效")
	v, _ = s.Status("youtube")
	if v.Usable || !v.PlatformEnabled {
		t.Fatal("YouTube应改为匿名", v)
	}
}

// TestCredentialRevisionGuards 新上传后旧错误和旧服务器Cookie都不能覆盖新值。
func TestCredentialRevisionGuards(t *testing.T) {
	s := New(t.TempDir())
	_ = s.Save("instagram", instagramFixture)
	// 捕获最初版本，模拟延迟到达的旧进程认证事件。
	old, _ := s.Status("instagram")
	_ = s.Save("instagram", strings.ReplaceAll(instagramFixture, "offline-session", "new-session"))
	// 保留版本信息，而不是依据SDK日志到达的先后认定最新凭据。
	changed, err := s.Disable("instagram", old.Revision, "旧请求认证失败")
	if changed || err != nil {
		t.Fatal(changed, err)
	}
	_ = s.Apply("instagram", old.Revision, []Cookie{{Name: "sessionid", Value: "old-session", Domain: ".instagram.com", Path: "/"}})
	raw, _ := os.ReadFile(s.Path("instagram"))
	if !strings.Contains(string(raw), "new-session") {
		t.Fatal("旧更新覆盖了新Cookie")
	}
	now, _ := s.Status("instagram")
	_ = s.Confirm("instagram", now.Revision)
	now, _ = s.Status("instagram")
	if now.State != "valid" {
		t.Fatal(now)
	}
	// 当前版本的正常Set-Cookie合并仍然有效，不以停用机制阻断续期。
	_ = s.Apply("instagram", now.Revision, []Cookie{{Name: "sessionid", Value: "rotated", Domain: ".instagram.com", Path: "/", Expires: time.Now().Add(time.Hour).Unix(), Secure: true}})
	raw, _ = os.ReadFile(s.Path("instagram"))
	if !strings.Contains(string(raw), "rotated") {
		t.Fatal(string(raw))
	}
}

// TestCookieExpiryAndFailureClassification 仅过期登录字段停用，不误判追踪字段或限流。
func TestCookieExpiryAndFailureClassification(t *testing.T) {
	// 期限来自Cookie文件；测试不需要访问Instagram验证。
	now := time.Now()
	if got := cookieReason("instagram", strings.Replace(instagramFixture, "\t0\t", "\t1\t", 1), now); got != "登录 Cookie 已过期" {
		t.Fatal(got)
	}
	if got := cookieReason("instagram", instagramFixture+".instagram.com\tTRUE\t/\tTRUE\t1\tcsrftoken\texpired\n", now); got != "" {
		t.Fatal(got)
	}
	// 内容权限、下载签名和429均可发生在有效登录中。
	for _, message := range []string{"429 Too Many Requests", "HTTP Error 403", "HTTP redirect to home page", "Sign in to confirm your age", "Sign in to confirm you’re not a bot", "private video"} {
		for _, p := range []string{"instagram", "youtube"} {
			if FailureReason(p, message) != "" {
				t.Fatal(p, message)
			}
		}
	}
	if FailureReason("instagram", "HTTP redirect to login page") == "" || FailureReason("youtube", "The provided YouTube account cookies are no longer valid.") == "" {
		t.Fatal("认证失败未被识别")
	}
}
