package domain

import (
	"strings"
	"testing"
)

// TestTaskMode 地址分类覆盖两个平台主页与详情及带列表参数的视频。
func TestTaskMode(t *testing.T) {
	for address, want := range map[string]string{
		"https://www.youtube.com/@author/videos":       "profile",
		"https://www.youtube.com/channel/UC123":        "profile",
		"https://www.youtube.com/watch?v=one&list=PL1": "content",
		"https://youtu.be/one":                         "content",
		"https://www.youtube.com/shorts/one":           "content",
		"https://www.instagram.com/author/":            "profile",
		"https://www.instagram.com/author/reels/":      "profile",
		"https://www.instagram.com/p/one/":             "content",
		"https://www.instagram.com/reel/one/":          "content",
	} {
		platform := "youtube"
		if strings.Contains(address, "instagram.com") {
			platform = "instagram"
		}
		if got := TaskMode(platform, address); got != want {
			t.Fatalf("%s: %s", address, got)
		}
	}
}
