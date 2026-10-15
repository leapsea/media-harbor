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

// AvatarHeaderBytes 与 HTTP 图片内容识别的最大探测长度一致。
const AvatarHeaderBytes = 512

// InstagramSourceCacheCapacity 只保留近期解析结果，避免随全量历史无限增长。
const InstagramSourceCacheCapacity = 64

// InstagramSourceCacheTTL 限制签名地址复用时间；地址失效时另行刷新一次。
const InstagramSourceCacheTTL = 60 * time.Minute

// InstagramMediaAttempts 只在媒体地址明确失效时重新解析，不放大 429 请求。
const InstagramMediaAttempts = 2

// InstagramRequestInterval 是所有 SDK 进程共享的最小请求间隔。
func InstagramRequestInterval() time.Duration {
	return seconds("INSTAGRAM_REQUEST_INTERVAL_SECONDS", 6*time.Second)
}

// InstagramCooldown 在未提供 Retry-After 时决定平台统一冷却时长。
func InstagramCooldown() time.Duration {
	return seconds("INSTAGRAM_429_COOLDOWN_SECONDS", 62*time.Second)
}

// seconds 允许隔离验收缩短等待，负数及非法值仍采用运行默认值。
func seconds(key string, fallback time.Duration) time.Duration {
	if value, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && value >= 0 {
		return time.Duration(value * float64(time.Second))
	}
	return fallback
}

// YouTubeMediaAttempts 在地址被拒绝或格式不可用后重新解析一次，不重复使用失效清单。
const YouTubeMediaAttempts = 2

// 评论上限和时限用于旧记录及直接引擎调用的缺省值，缺省字段不能回到无限抓取。
const YouTubeCommentLimit = 100
const YouTubeCommentTimeoutSeconds = 180

// CommentProgressBatch 以新增条数推进日志；慢速采集也按既有等待报告间隔输出进展。
const CommentProgressBatch = 10

// CommentDefaults 只补齐新增字段，显式配置与其他下载选项保持原样。
func CommentDefaults(o domain.YouTubeOptions) domain.YouTubeOptions {
	if o.CommentLimit == 0 {
		o.CommentLimit = YouTubeCommentLimit
	}
	if o.CommentTimeoutSeconds == 0 {
		o.CommentTimeoutSeconds = YouTubeCommentTimeoutSeconds
	}
	return o
}

// YouTubeFormatSort 以视频存在性、分辨率、帧率为顺序，不让来源优先级压过尺寸。
const YouTubeFormatSort = "hasvid,res,fps"

// YouTubeFilenameStem 按 UTF-8 字节限制标题，为格式编号、分片、字幕及音频后缀预留空间。
// 只缩短磁盘文件名中的标题，完整 ID 保持同标题视频之间的唯一性。
// 历史资产：各阶段使用 %(title)s-%(id)s，长中文标题会使临时文件超过文件系统限制。
const YouTubeFilenameStem = "%(filename_title).160B-%(id)s"

// YouTubeFilenameTitle 让 SDK 先清理文件名副本，避免冒号、斜杠等转换为全角字符后再次超长。
// 原 title 字段保持完整，仅输出模板读取该副本。
const YouTubeFilenameTitle = "%(title)S:%(filename_title)s"

// YouTubeMetadataCacheTTL 保留近期格式清单，403 与格式不可用仍走串行生产者刷新。
const YouTubeMetadataCacheTTL = 15 * time.Minute
const YouTubeMetadataCacheCapacity = 128

// ScanQueueCapacity 限制解析缓冲和每配置提前入队数量。
const ScanQueueCapacity = 16

// ScanQueueCheckInterval 等待下载消费者释放持久队列容量。
const ScanQueueCheckInterval = time.Second

// SchedulerDiagnosticInterval 在调度被锁或存储阻塞时周期记录最后阶段。
const SchedulerDiagnosticInterval = 30 * time.Second

// TaskDetailsSlowThreshold 只记录缓慢的详情读取，避免每秒刷新产生重复日志。
const TaskDetailsSlowThreshold = 500 * time.Millisecond

// ScanLogInterval 控制扫描等待期间的可见进度频率。
const ScanLogInterval = 5 * time.Second

// ScanLogIdleInterval 仅在长时间没有日志进展时补充扫描状态，避免与请求等待重复。
const ScanLogIdleInterval = 15 * time.Second

// RequestWaitLogInterval 保留长冷却进度，同一次等待不再输出末尾毫秒倒计时。
const RequestWaitLogInterval = 15 * time.Second
const RequestWaitLogThreshold = time.Second

// TaskWarningLimit 限制单任务的警告去重记录；超过范围的新警告仍完整输出。
const TaskWarningLimit = 128

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
	// 旧环境参数仅初始化新字段；之后以系统设置中的持久值为准。
	settings.Pipeline.Instagram.MinIntervalSeconds = InstagramRequestInterval().Seconds()
	settings.Pipeline.Instagram.CooldownSeconds = InstagramCooldown().Seconds()
	settings.Sleep.Instagram.Timezone = env("TZ", settings.Sleep.Instagram.Timezone)
	settings.Sleep.YouTube.Timezone = env("TZ", settings.Sleep.YouTube.Timezone)
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
