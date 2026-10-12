package engine

import (
	"net/url"
	"testing"
)

// TestInstagramHTMLName 覆盖页面作者数据、主页链接、转义 JSON 与多个账号同时出现的情况。
func TestInstagramHTMLName(t *testing.T) {
	address, _ := url.Parse("https://www.instagram.com/p/DeHR060yl2F/")
	for _, tc := range []struct{ name, page, want string }{
		{"username", `<script>{"username": "yen_210"}</script>`, "yen_210"},
		{"profile", `<a href="https://www.instagram.com/yen_210/">Profile</a>`, "yen_210"},
		{"escaped", `<script>"{\"username\":\"yen_210\"}"</script>`, "yen_210"},
		{"current-post", `<script>{"viewer":{"username":"viewer"},"media":{"shortcode":"DeHR060yl2F","owner":{"username":"yen_210"}},"recommendation":{"username":"other"}}</script>`, "yen_210"},
		{"linked-author", `{"username":"viewer"} {"username":"yen_210"}<a href="https://www.instagram.com/yen_210">Author</a>`, "yen_210"},
		{"ambiguous", `{"username":"first"} {"username":"second"}`, ""},
		{"content-is-not-author", `https://www.instagram.com/p/DeHR060yl2F/`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := instagramNameFromHTML(tc.page, address); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
