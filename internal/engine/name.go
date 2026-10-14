package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/http"
	"net/url"
	"strings"
)

// ResolveName 仅读取作者信息；Instagram 内容直接解析网页，不枚举媒体。
// 主页账号可直接从地址取得，内容短码不能用于目录名称。
func (c CLI) ResolveName(ctx context.Context, s domain.Subscription) (string, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if s.Platform == "instagram" && len(parts) > 0 && parts[0] != "" {
		switch parts[0] {
		case "p", "reel", "reels", "tv", "stories":
		default:
			return parts[0], nil
		}
	}
	// YouTube 视频使用 oEmbed 作者名，避免等待格式与签名提取。
	if s.Platform == "youtube" && (u.Query().Get("v") != "" || u.Hostname() == "youtu.be" || parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, config.YouTubeMetadataURL+"?format=json&url="+url.QueryEscape(s.URL), nil)
		if err != nil {
			return "", err
		}
		var release func()
		if p := c.pipeline("youtube"); p != nil {
			var err error
			release, err = p.acquireProducer(ctx, func(string) {})
			if err != nil {
				return "", err
			}
			defer release()
			release, err = p.requests.wait(ctx, func(string) {}, "author")
			if err != nil {
				return "", err
			}
			defer release()
		}
		response, err := http.DefaultClient.Do(req)
		if response != nil && response.StatusCode == 429 {
			if p := c.pipeline("youtube"); p != nil {
				p.requests.rateLimited(response.Header.Get("Retry-After"))
			}
		}
		if err != nil {
			return "", fmt.Errorf("读取 YouTube 作者失败：%w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("YouTube 作者信息不可用（HTTP %d）", response.StatusCode)
		}
		var metadata struct {
			AuthorName string `json:"author_name"`
		}
		if err = json.NewDecoder(response.Body).Decode(&metadata); err != nil {
			return "", err
		}
		if metadata.AuthorName != "" {
			return metadata.AuthorName, nil
		}
		return "", fmt.Errorf("YouTube 未返回作者名称")
	}
	// 频道及播放列表只取首项元数据；标题不是作者，不能用列表标题替代。
	if s.Platform == "youtube" {
		args := append(c.common(s), "--playlist-end", "1", "--dump-single-json", "--flat-playlist", "--", s.URL)
		raw, err := c.runYouTubeProducer(ctx, args, func(string) {}, true)
		if err != nil {
			return "", err
		}
		var entry youtubeEntry
		if err = json.Unmarshal(raw, &entry); err != nil {
			return "", err
		}
		name := youtubeAuthor(entry)
		if name == "" {
			return "", fmt.Errorf("YouTube 未返回作者名称，请填写目录名称")
		}
		return name, nil
	}
	if s.Platform == "instagram" {
		return c.instagramPageName(ctx, s.URL)
	}
	return "", fmt.Errorf("当前平台不支持作者名称解析")
}

// youtubeAuthor 优先平台频道名，再读取上传者和首项的所属频道。
func youtubeAuthor(entry youtubeEntry) string {
	if entry.Channel != "" {
		return entry.Channel
	}
	if entry.Uploader != "" {
		return entry.Uploader
	}
	if len(entry.Entries) > 0 {
		return youtubeAuthor(entry.Entries[0])
	}
	return ""
}

// ResolveAuthor 只解析 Instagram 头像元数据，主页与详情取得同一稳定作者身份。
// dump-json 不下载媒体；头像文件在任务保存成功后由应用服务获取。
func (c CLI) ResolveAuthor(ctx context.Context, s domain.Subscription) (domain.ScanResult, error) {
	name, err := c.ResolveName(ctx, s)
	if err != nil {
		return domain.ScanResult{}, err
	}
	if s.Platform != "instagram" {
		return domain.ScanResult{CurrentName: name}, nil
	}
	if p := c.pipeline("instagram"); p != nil {
		release, err := p.acquireProducer(ctx, func(string) {})
		if err != nil {
			return domain.ScanResult{}, err
		}
		defer release()
	}
	args := append(c.common(s), "--dump-json", "--range", "1", "--", "https://www.instagram.com/"+url.PathEscape(name)+"/avatar/")
	env, end, err := c.instagramController().begin(ctx, "author", func(string) {})
	if err != nil {
		return domain.ScanResult{}, err
	}
	defer end()
	// 头像作者解析也走统一请求许可，不能绕过当前平台冷却。
	script := instagramScript("import sys,gallery_dl\nsys.exit(gallery_dl.main())\n")
	args = append([]string{"-u", "-c", script}, args...)
	raw, err := runWithEnv(ctx, "python3", args, func(string) {}, true, env)
	if err != nil {
		return domain.ScanResult{}, fmt.Errorf("解析头像作者失败：%w", err)
	}
	author, err := parseInstagram(raw)
	if err != nil {
		return domain.ScanResult{}, err
	}
	if author.StableID == "" || author.CurrentName == "" {
		return domain.ScanResult{}, fmt.Errorf("头像元数据未返回稳定作者身份")
	}
	author.Items = nil
	return author, nil
}
