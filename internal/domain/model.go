// Package domain 定义订阅、内容和任务，与 HTTP 及具体下载工具无关。
package domain

import (
	"context"
	"time"
)

// 音频下载与音频提取是两个独立布尔值。
// 评论在 V1 使用引擎的 info JSON 文件。
// YouTubeOptions 可用于全局默认值或订阅级整体覆盖。
type YouTubeOptions struct {
	Video             bool   `json:"video"`
	Format            string `json:"format"`
	Subtitles         bool   `json:"subtitles"`
	AutoSubtitles     bool   `json:"auto_subtitles"`
	SubtitleLanguages string `json:"subtitle_languages"`
	Comments          bool   `json:"comments"`
	ExtraAudio        bool   `json:"extra_audio"`
	ExtractAudio      bool   `json:"extract_audio"`
	AudioFormat       string `json:"audio_format"`
	AudioQuality      string `json:"audio_quality"`
	Thumbnail         bool   `json:"thumbnail"`
	Metadata          bool   `json:"metadata"`
	Fragments         int    `json:"fragments"`
}

// Subscription 的 DisplayName 固定，CurrentName 在扫描时更新。
type Subscription struct {
	ID          int64           `json:"id"`
	Platform    string          `json:"platform"`
	URL         string          `json:"url"`
	Mode        string          `json:"mode"`
	StableID    string          `json:"stable_id"`
	DisplayName string          `json:"display_name"`
	CurrentName string          `json:"current_name"`
	Enabled     bool            `json:"enabled"`
	Schedule    string          `json:"schedule"`
	Template    string          `json:"template"`
	Archive     bool            `json:"archive"`
	YouTube     *YouTubeOptions `json:"youtube"`
	LastScan    time.Time       `json:"last_scan"`
	LastSuccess time.Time       `json:"last_success"`
	NextRun     time.Time       `json:"next_run"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ContentFile 将每个真实文件与其产物标签关联，评论和元数据可共用一个 JSON 文件。
type ContentFile struct {
	Path string   `json:"path"`
	Tags []string `json:"tags"`
	// 评论数量按文件中实际保存的数组统计，未获取评论时不返回数量。
	CommentCount *int `json:"comment_count,omitempty"`
}

// Content 是下载去重的最小单元；Instagram 轮播的每张媒体具有独立 ID。
type Content struct {
	ID             int64         `json:"id"`
	SubscriptionID int64         `json:"subscription_id"`
	RemoteID       string        `json:"remote_id"`
	URL            string        `json:"url"`
	Title          string        `json:"title"`
	Published      time.Time     `json:"published"`
	Asset          int           `json:"asset"`
	Status         string        `json:"status"`
	Paths          []string      `json:"paths"`
	Tags           []string      `json:"tags"`
	Files          []ContentFile `json:"files"`
	Archived       bool          `json:"archived"`
	Error          string        `json:"error"`
}

// queued、running、completed、failed、cancelled 是持久任务状态。
// Task 的状态持久化，运行日志单独存储于运行数据目录。
type Task struct {
	ID             int64     `json:"id"`
	SubscriptionID int64     `json:"subscription_id"`
	ContentID      int64     `json:"content_id"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	Error          string    `json:"error"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
}

// UISettings 管理界面语言和展示数量，不改变扫描和下载任务的执行范围。
type UISettings struct {
	Language        string `json:"language"`
	RecentDownloads int    `json:"recent_downloads"`
	HistoryPageSize int    `json:"history_page_size"`
	TaskPageSize    int    `json:"task_page_size"`
}

// DebugSettings 控制测试批次，0 表示不限条目。
type DebugSettings struct {
	TestItemLimit int `json:"test_item_limit"`
}

// Settings 中的并发上限由调度器实际执行。
type Settings struct {
	YouTubeConcurrent   int            `json:"youtube_concurrent"`
	InstagramConcurrent int            `json:"instagram_concurrent"`
	Template            string         `json:"template"`
	AutoArchive         bool           `json:"auto_archive"`
	UseUploadDate       bool           `json:"use_upload_date"`
	YouTube             YouTubeOptions `json:"youtube"`
	UI                  UISettings     `json:"ui"`
	Debug               DebugSettings  `json:"debug"`
}

// ScanResult 仅表示引擎在本次扫描中实际可获取的内容。
type ScanResult struct {
	StableID    string
	CurrentName string
	Items       []Content
}

// Engine 把扫描及下载能力隔离，应用服务无需依赖命令行实现。
type Engine interface {
	Scan(context.Context, Subscription, int, func(string), func(ScanResult) error) (ScanResult, error)
	Download(context.Context, Subscription, Content, YouTubeOptions, string, func(string)) error
}

// NameResolver 只读取作者元数据，不创建订阅或下载任务。
type NameResolver interface {
	ResolveName(context.Context, Subscription) (string, error)
}

// AvatarDownloader 是可选的作者头像能力，不把头像加入媒体下载队列。
type AvatarDownloader interface {
	UpdateAvatar(context.Context, Subscription, func(string)) error
}

// DownloadHistoryRepository 隔离历史展示生命周期，不删除下载索引或文件。
type DownloadHistoryRepository interface {
	DownloadHistory() ([]Content, error)
	ClearDownloadHistory() (int, error)
	ShowDownloadHistory(int64) error
}

// Repository 隔离持久化；不采用数据库外键或触发器。
type Repository interface {
	DownloadHistoryRepository
	Subscriptions() ([]Subscription, error)
	Subscription(int64) (Subscription, error)
	SaveSubscription(*Subscription) error
	DeleteSubscription(int64) error
	Contents() ([]Content, error)
	Content(int64) (Content, error)
	SaveContent(*Content) error
	Tasks() ([]Task, error)
	Task(int64) (Task, error)
	SaveTask(*Task) error
	Enqueue(int64, int64, string) (Task, error)
	AddContentAndTask(int64, Content) error
	Settings() (Settings, error)
	SaveSettings(Settings) error
}
