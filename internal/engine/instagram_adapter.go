package engine

import _ "embed"

// instagramAdapter 在各执行入口共用同一详情网页解析策略。
//
//go:embed instagram_adapter.py
var instagramAdapter string

// 历史入口：go:embed instagram_progress.py / var instagramProgress string。
// 该脚本保留参考，正式传输进度由 Go 处理，不再嵌入二进制。

// instagramTransport 只适配 SDK 请求许可，节流及冷却算法位于 Go。
//
//go:embed instagram_transport.py
var instagramTransport string

func instagramScript(script string) string {
	return instagramTransport + "\ninstall_instagram_transport()\n" + instagramAdapter + "\ninstall_instagram_adapter()\n" + script
}
