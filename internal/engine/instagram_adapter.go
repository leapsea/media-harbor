package engine

import _ "embed"

// instagramAdapter 在各执行入口共用同一详情网页解析策略。
//
//go:embed instagram_adapter.py
var instagramAdapter string

func instagramScript(script string) string {
	return instagramAdapter + "\ninstall_instagram_adapter()\n" + script
}
