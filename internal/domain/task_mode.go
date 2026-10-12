package domain

import (
	"net/url"
	"strings"
)

// TaskMode 区分作者主页与单条内容，详情地址不会进入周期调度。
// watch 地址即使带播放列表参数，仍按当前视频立即执行。
func TaskMode(platform, address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return "profile"
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if platform == "youtube" {
		if u.Query().Get("v") != "" || (u.Hostname() == "youtu.be" && parts[0] != "") {
			return "content"
		}
		if len(parts) > 1 {
			switch parts[0] {
			case "shorts", "live", "embed":
				return "content"
			}
		}
	}
	if platform == "instagram" && len(parts) > 1 {
		switch parts[0] {
		case "p", "reel", "reels", "tv":
			return "content"
		}
		if parts[0] == "stories" && len(parts) > 2 {
			return "content"
		}
	}
	return "profile"
}
