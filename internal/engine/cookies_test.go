package engine

import (
	"strings"
	"testing"
)

// TestNormalizeCookies 验证请求头转换后的域、七列格式及凭据值不被改写。
func TestNormalizeCookies(t *testing.T) {
	for _, platform := range []string{"instagram", "youtube"} {
		t.Run(platform, func(t *testing.T) {
			raw, err := NormalizeCookies(platform, "Cookie: sessionid=test%3Avalue; token=abc=def; empty=")
			if err != nil {
				t.Fatal(err)
			}
			rows := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
			if len(rows) != 4 {
				t.Fatal("Cookie 条数错误")
			}
			for _, row := range rows[1:] {
				fields := strings.Split(row, "\t")
				if len(fields) != 7 || fields[0] != "."+platform+".com" || fields[4] != "0" {
					t.Fatal("Netscape 格式错误")
				}
			}
			if !strings.HasSuffix(rows[1], "\tsessionid\ttest%3Avalue") || !strings.HasSuffix(rows[2], "\ttoken\tabc=def") {
				t.Fatal("Cookie 值被改写")
			}
		})
	}
	raw := "# Netscape HTTP Cookie File\n#HttpOnly_.instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\ttest\n"
	if got, err := NormalizeCookies("instagram", raw); err != nil || got != raw {
		t.Fatal("Netscape 内容应原样保留")
	}
	if _, err := NormalizeCookies("instagram", "not a cookie"); err == nil {
		t.Fatal("应提示输入 Cookie 格式")
	}
}
