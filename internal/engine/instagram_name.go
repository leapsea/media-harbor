package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"media-harbor/internal/config"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var instagramScripts = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script\s*>`)
var instagramUsername = regexp.MustCompile(`"username"\s*:\s*"([A-Za-z0-9_.]+)"`)
var instagramProfile = regexp.MustCompile(`https?://(?:www\.)?instagram\.com/([A-Za-z0-9_.]+)/?(?:["'\s<\\]|$)`)

// instagramPageName 请求原始帖子页面，只返回作者账号，不下载文件或创建任务。
func (c CLI) instagramPageName(ctx context.Context, address string) (string, error) {
	if p := c.pipeline("instagram"); p != nil {
		release, err := p.acquireProducer(ctx, func(string) {})
		if err != nil {
			return "", err
		}
		defer release()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", config.InstagramRequestUserAgent())
	req.Header.Set("Accept", config.InstagramPageAccept)
	var headers map[string]string
	_ = json.Unmarshal([]byte(config.InstagramHeaders), &headers)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	// 名称解析使用公开网页，不发送系统 Cookie。
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	// 每次重定向也经过共享许可，仍沿用 Go 原有的公开网页传输实现。
	client := &http.Client{Transport: instagramRoundTripper{c.instagramController()}}
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("读取 Instagram 帖子网页失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Instagram 网页不可用（HTTP %d）", response.StatusCode)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	if name := instagramNameFromHTML(string(raw), req.URL); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("Instagram 网页未返回可确认的帖子作者，请填写目录名称")
}

// instagramPostAuthor 先匹配当前短码，再读取该帖子 owner/user 的账号，避免取到登录者或推荐账号。
func instagramPostAuthor(value any, shortcode string) string {
	switch v := value.(type) {
	case map[string]any:
		if v["shortcode"] == shortcode || v["code"] == shortcode {
			for _, key := range []string{"owner", "user", "author"} {
				if owner, ok := v[key].(map[string]any); ok {
					if name, ok := owner["username"].(string); ok && name != "" {
						return name
					}
				}
			}
			if name, ok := v["username"].(string); ok {
				return name
			}
		}
		for _, child := range v {
			if name := instagramPostAuthor(child, shortcode); name != "" {
				return name
			}
		}
	case []any:
		for _, child := range v {
			if name := instagramPostAuthor(child, shortcode); name != "" {
				return name
			}
		}
	}
	return ""
}

// instagramNameFromHTML 优先帖子结构数据，其次使用网页中明确的 username 与主页链接。
// 没有帖子结构且存在多个不同账号时不任取第一个，防止目录误用登录账号。
func instagramNameFromHTML(page string, address *url.URL) string {
	parts := strings.Split(strings.Trim(address.Path, "/"), "/")
	shortcode := parts[len(parts)-1]
	page = html.UnescapeString(page)
	for _, script := range instagramScripts.FindAllStringSubmatch(page, -1) {
		var value any
		if json.Unmarshal([]byte(script[1]), &value) == nil {
			if name := instagramPostAuthor(value, shortcode); name != "" {
				return name
			}
		}
	}
	// 页面内的 JSON 字符串可能转义引号和斜杠，归一化后提取账号。
	page = strings.ReplaceAll(strings.ReplaceAll(page, `\"`, `"`), `\/`, `/`)
	names := map[string]bool{}
	for _, match := range instagramUsername.FindAllStringSubmatch(page, -1) {
		names[match[1]] = true
	}
	profiles := map[string]bool{}
	for _, match := range instagramProfile.FindAllStringSubmatch(page, -1) {
		switch match[1] {
		case "p", "reel", "reels", "tv", "stories", "accounts", "explore", "about", "legal", "developer":
			continue
		}
		profiles[match[1]] = true
	}
	linked := map[string]bool{}
	for name := range names {
		if profiles[name] {
			linked[name] = true
		}
	}
	if len(linked) == 1 {
		for name := range linked {
			return name
		}
	}
	if len(names) == 1 {
		for name := range names {
			return name
		}
	}
	if len(names) == 0 && len(profiles) == 1 {
		for name := range profiles {
			return name
		}
	}
	return ""
}
