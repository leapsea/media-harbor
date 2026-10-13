// Package config 集中管理运行环境，容器和本地开发共用同一入口。
package config

import (
	_ "embed"
	"encoding/json"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// DownloadLogInterval 限制重复进度输出，不影响错误和最终结果。
const DownloadLogInterval = time.Second

// YouTubeMediaAttempts 在媒体地址被拒绝后重新解析一次，不反复重试同一个签名地址。
const YouTubeMediaAttempts = 2

// ScanQueueCapacity 限制解析缓冲和每配置提前入队数量。
const ScanQueueCapacity = 32

// ScanQueueCheckInterval 等待下载消费者释放持久队列容量。
const ScanQueueCheckInterval = time.Second

// SchedulerDiagnosticInterval 在调度被锁或存储阻塞时周期记录最后阶段。
const SchedulerDiagnosticInterval = 30 * time.Second

// ScanLogInterval 控制扫描等待期间的可见进度频率。
const ScanLogInterval = 5 * time.Second

// LogTailReadChunkBytes 按块从文件尾部读取，日志窗口不扫描全部历史内容。
const LogTailReadChunkBytes = 32 * 1024

// DryRunItemLimit 在未启用调试限制时只验证前十个条目，避免枚举全部历史。
const DryRunItemLimit = 10

// DryRunProbeBytes 验证远端响应可读取，内容只留在内存中并立即丢弃。
const DryRunProbeBytes = 64 * 1024

// DryRunManifestDepth 允许探测主清单、媒体清单及首个媒体分片。
const DryRunManifestDepth = 3

// YouTubeMetadataURL 使用官方公开作者信息，视频解析无需下载媒体。
const YouTubeMetadataURL = "https://www.youtube.com/oembed"

// InstagramUserAgent 匹配桌面 Chrome，可用环境变量覆盖以适配登录会话。
const InstagramUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36"

// InstagramRequestUserAgent 让网页解析与下载引擎共用环境覆盖规则。
func InstagramRequestUserAgent() string {
	if value := os.Getenv("INSTAGRAM_USER_AGENT"); value != "" {
		return value
	}
	return InstagramUserAgent
}

// InstagramPageAccept 请求帖子 HTML，区别于引擎内部的 JSON API 请求。
const InstagramPageAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"

// InstagramHeaders 只补充通用请求头；HTML 与 API 的 Accept、请求类型由引擎按接口设置。
const InstagramHeaders = `{"Accept-Language":"zh-CN,zh;q=0.9","Referer":"https://www.instagram.com/"}`

// AACBestQuality 与 yt-dlp 的 AAC 最高质量档一致。
const AACBestQuality = "4"

// Config 的目录参数允许容器不挂载卷时使用内部目录。
type Config struct {
	Address, DataDir, LogDir, DownloadDir, ArchiveDir, WebDir string
}

// Load 读取环境变量；无需在容器启动时修改宿主机所有权。
func Load() Config {
	dataDir := env("DATA_DIR", "./data")
	return Config{Address: env("ADDRESS", ":8080"), DataDir: dataDir, LogDir: env("LOG_DIR", filepath.Join(dataDir, "logs")), DownloadDir: env("DOWNLOAD_DIR", "./downloads"), ArchiveDir: env("ARCHIVE_DIR", "./archive"), WebDir: env("WEB_DIR", "./web/dist")}
}

// initialSettings 保存用户确认的初始化参数，随程序构建分发。
//
//go:embed initial-settings.json
var initialSettings []byte

// Defaults 只在新数据库首次启动时生效，环境变量仍可覆盖并发及归档。
func Defaults() domain.Settings {
	var settings domain.Settings
	_ = json.Unmarshal(initialSettings, &settings)
	settings.YouTubeConcurrent = number("YTDLP_MAX_CONCURRENT", settings.YouTubeConcurrent)
	settings.InstagramConcurrent = number("GALLERY_DL_MAX_CONCURRENT", settings.InstagramConcurrent)
	return settings
}

// env 和 number 仅承担配置读取，不建立额外配置框架。
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func number(key string, fallback int) int {
	v, e := strconv.Atoi(os.Getenv(key))
	if e == nil && v > 0 {
		return v
	}
	return fallback
}
