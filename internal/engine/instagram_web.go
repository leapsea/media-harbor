package engine

import (
	"encoding/json"
	"strings"
)

// instagramWebPost 只读取当前短码的完整媒体对象，排除登录者与推荐帖子。
// 返回 SDK 原始结构，不另定义一套平台媒体模型。
func instagramWebPost(page, shortcode string) map[string]any {
	var find func(any) map[string]any
	find = func(value any) map[string]any {
		switch v := value.(type) {
		case map[string]any:
			if v["code"] == shortcode && v["pk"] != nil && v["user"] != nil && v["image_versions2"] != nil {
				return v
			}
			for _, child := range v {
				if post := find(child); post != nil {
					return post
				}
			}
		case []any:
			for _, child := range v {
				if post := find(child); post != nil {
					return post
				}
			}
		}
		return nil
	}
	// 已有作者解析器提供 script 提取规则，复用以保持帖子网页解析一致。
	for _, script := range instagramScripts.FindAllStringSubmatch(page, -1) {
		var value any
		decoder := json.NewDecoder(strings.NewReader(script[1]))
		// 平台整数 ID 可能超出 float64 精确范围，原样交回 SDK。
		decoder.UseNumber()
		if decoder.Decode(&value) == nil {
			// 非 JSON 脚本不影响后续结构化页面对象的查找。
			if post := find(value); post != nil {
				return post
			}
		}
	}
	return nil
}
