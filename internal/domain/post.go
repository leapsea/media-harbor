package domain

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PostTask 是 B 层；清单完整且所有期望的 C 层资源完成后才标记完成。
// 旧索引无法证明轮播总数时保留清单待确认，不能只凭第一张图片跳过帖子。
type PostTask struct {
	ID               int64     `json:"id"`
	SubscriptionID   int64     `json:"subscription_id"`
	RemoteID         string    `json:"remote_id"`
	URL              string    `json:"url"`
	Title            string    `json:"title"`
	Status           string    `json:"status"`
	Kinds            []string  `json:"kinds"`
	ManifestComplete bool      `json:"manifest_complete"`
	Expected         int       `json:"expected"`
	Completed        int       `json:"completed"`
	Error            string    `json:"error,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DownloadItem 是 C 层；一个轮播资源或视频的一个可选产物拥有独立持久状态。
// ContentID 保持现有文件索引和执行记录的身份，升级不移动媒体或改写下载路径。
type DownloadItem struct {
	ID        int64            `json:"id"`
	PostID    int64            `json:"post_id"`
	ContentID int64            `json:"content_id"`
	Progress  TransferProgress `json:"progress"`
	Required  bool             `json:"required"`
	Kind      string           `json:"kind"`
	Status    string           `json:"status"`
	Outcome   string           `json:"outcome,omitempty"`
	Error     string           `json:"error,omitempty"`
	Paths     []string         `json:"paths"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// PostIdentity 统一 URL 的短码/视频 ID，不让轮播序号、追踪参数或 URL 别名拆散 B。
func PostIdentity(platform string, item Content) string {
	u, err := url.Parse(item.URL)
	if err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if platform == "instagram" && len(parts) > 1 && (parts[0] == "p" || parts[0] == "reel" || parts[0] == "reels" || parts[0] == "tv") {
			return parts[1]
		}
		if platform == "youtube" {
			if id := u.Query().Get("v"); id != "" {
				return id
			}
			if u.Hostname() == "youtu.be" && len(parts) > 0 {
				return parts[0]
			}
			if len(parts) > 1 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") {
				return parts[1]
			}
		}
	}
	if platform == "instagram" {
		return strings.TrimSuffix(item.RemoteID, "-"+strconv.Itoa(item.Asset))
	}
	return item.RemoteID
}

// ExpectedKinds 描述用户选择的产物；字幕不存在等结果由执行器显式记录 unavailable。
func ExpectedKinds(platform string, o YouTubeOptions) []string {
	if platform == "instagram" {
		return []string{"media"}
	}
	kinds := []string{}
	if o.Video || o.ExtractAudio {
		kinds = append(kinds, "video")
	}
	if o.Metadata {
		kinds = append(kinds, "metadata")
	}
	if o.Comments {
		kinds = append(kinds, "comments")
	}
	if o.Subtitles || o.AutoSubtitles {
		kinds = append(kinds, "subtitles")
	}
	if o.Thumbnail {
		kinds = append(kinds, "thumbnail")
	}
	if o.ExtraAudio {
		kinds = append(kinds, "audio")
	}
	if o.ExtractAudio {
		kinds = append(kinds, "extracted")
	}
	return kinds
}

// PostGate 由应用服务核对 B，SDK 只报告原始帖子身份并执行返回的 continue/skip/stop。
type PostCandidate struct {
	RemoteID string
	URL      string
}
type PostGate func(PostCandidate) (string, error)
type postGateKey struct{}

func WithPostGate(ctx context.Context, gate PostGate) context.Context {
	return context.WithValue(ctx, postGateKey{}, gate)
}
func GatePost(ctx context.Context, post PostCandidate) (string, error) {
	if gate, ok := ctx.Value(postGateKey{}).(PostGate); ok {
		return gate(post)
	}
	return "continue", nil
}

// PostScanner 声明扫描器在资源解析前支持 B 层核对，保留旧工具的流式兼容入口。
type PostScanner interface{ SupportsPostTasks() bool }

// PostDownloadRow 是任务详情唯一的数据源，B/C 为主体，执行账本只提供操作与排序信息。
type PostDownloadRow struct {
	Post     PostTask
	Download DownloadItem
	Content  Content
	Task     Task
}
