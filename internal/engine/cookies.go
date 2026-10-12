package engine

import (
	"fmt"
	"strings"
)

// NormalizeCookies 将浏览器 Cookie 请求头转成下载引擎需要的 Netscape 格式。
// 已有 Netscape 文件原样保留；值不解码，避免改变 sessionid 等凭据内容。
func NormalizeCookies(platform, content string) (string, error) {
	if strings.Contains(content, "\t") || strings.HasPrefix(strings.TrimSpace(content), "# Netscape") {
		return content, nil
	}
	domain := map[string]string{"instagram": ".instagram.com", "youtube": ".youtube.com"}[platform]
	if domain == "" {
		return "", fmt.Errorf("平台无效")
	}
	header := strings.TrimSpace(content)
	if len(header) >= len("Cookie:") && strings.EqualFold(header[:len("Cookie:")], "Cookie:") {
		header = strings.TrimSpace(header[len("Cookie:"):])
	}
	var output strings.Builder
	output.WriteString("# Netscape HTTP Cookie File\n")
	count := 0
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("请粘贴 Cookie 请求头（name=value; ...）或 Netscape 格式内容")
		}
		fmt.Fprintf(&output, "%s\tTRUE\t/\tTRUE\t0\t%s\t%s\n", domain, strings.TrimSpace(name), strings.TrimSpace(value))
		count++
	}
	if count == 0 {
		return "", fmt.Errorf("Cookie 内容为空")
	}
	return output.String(), nil
}
