// Package domain 定义订阅、内容和任务，与 HTTP 及具体下载工具无关。
package domain

import (
	"context"
	"errors"
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
	// RequestLanguage 由执行时的系统设置提供，不作为任务配置保存或暴露给 API。
	RequestLanguage string          `json:"-"`
	ID              int64           `json:"id"`
	Platform        string          `json:"platform"`
	URL             string          `json:"url"`
	Mode            string          `json:"mode"`
	StableID        string          `json:"stable_id"`
	DisplayName     string          `json:"display_name"`
	CurrentName     string          `json:"current_name"`
	Enabled         bool            `json:"enabled"`
	Schedule        string          `json:"schedule"`
	Template        string          `json:"template"`
	Archive         bool            `json:"archive"`
	YouTube         *YouTubeOptions `json:"youtube"`
	LastScan        time.Time       `json:"last_scan"`
	LastSuccess     time.Time       `json:"last_success"`
	NextRun         time.Time       `json:"next_run"`
	CreatedAt       time.Time       `json:"created_at"`
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
	Overwrite      bool          `json:"-"`                 // 全量生产者要求刷新解析；文件位置仍由持久索引维护。
	PostID         int64         `json:"post_id,omitempty"` // 所属帖子任务，原内容 ID 继续对应文件索引。
	ExpectedAssets int           `json:"expected_assets"`   // SDK 提供的整帖资源总数，0 表示未知。
	SourceTaskID   int64         `json:"-"`                 // 本次扫描来源只用于关联下载批次，不进入内容索引。
	ChannelID      string        `json:"channel_id,omitempty"`
	Availability   string        `json:"availability,omitempty"`
	MediaType      string        `json:"media_type,omitempty"`
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

// IsPaidAvailability 只接受平台的付费标识，普通登录、年龄或地区限制不属于付费视频。
func IsPaidAvailability(value string) bool {
	return value == "subscriber_only" || value == "premium_only"
}

// PaidVideoPolicy 是下载器的只读权限决策，不向引擎暴露整个仓储。
type PaidVideoPolicy interface{ PaidFailure(string) (int64, error) }

// PaidVideoSkippedError 表示策略跳过，不是下载失败或下载完成。
type PaidVideoSkippedError struct {
	ChannelID    string
	FailedTaskID int64
}

func (e *PaidVideoSkippedError) Error() string {
	return "跳过付费视频：该 UP 主此前有付费视频下载失败"
}

// queued、running、completed、failed、cancelled、skipped 是持久任务状态。
// Task 的状态持久化，运行日志单独存储于运行数据目录。
type Task struct {
	Overwrite      bool               `json:"overwrite,omitempty"` // 全量下载跨重启仍不复用初始化前的文件。
	QueuedAt       time.Time          `json:"queued_at,omitempty"` // 重试仍保留任务 ID，但使用本次入队时间排序。
	SourceTaskID   int64              `json:"source_task_id,omitempty"`
	Progress       []TransferProgress `json:"progress,omitempty"`
	ID             int64              `json:"id"`
	SubscriptionID int64              `json:"subscription_id"`
	ContentID      int64              `json:"content_id"`
	Kind           string             `json:"kind"`
	Status         string             `json:"status"`
	Attempts       int                `json:"attempts"`
	Error          string             `json:"error"`
	CreatedAt      time.Time          `json:"created_at"`
	StartedAt      time.Time          `json:"started_at"`
	FinishedAt     time.Time          `json:"finished_at"`
}

// PlatformRun 保存平台当前批次；重启后先继续该批次，再处理等待的博主。
type PlatformRun struct {
	Platform       string    `json:"platform"`
	SubscriptionID int64     `json:"subscription_id"`
	ScanTaskID     int64     `json:"scan_task_id"`
	ScanTaskIDs    []int64   `json:"scan_task_ids,omitempty"` // 当前批次已有扫描和下载来源，后续扫描不加入。
	StartedAt      time.Time `json:"started_at"`
}

// TaskContentRow 将任务与详情展示所需的内容快照关联。
type TaskContentRow struct {
	Task    Task
	Content Content
}

// TaskGroupStats 是任务管理列表使用的轻量汇总投影。
type TaskGroupStats struct {
	SubscriptionID int64
	Counts         map[string]int
	Total          int
	ScanStatus     string
	UpdatedAt      time.Time
	Error          string
	Queued         int
	Running        int
	Failed         int
	Cancelled      int
}

// TransferProgress 是一个视频或帖子媒体的实际传输状态，不单独占用队列名额。
type TransferProgress struct {
	Title      string  `json:"title,omitempty"`
	Phase      string  `json:"phase,omitempty"` // 等待、解析与传输分开，零字节不等于正在下载。
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	BytesTotal int64   `json:"bytes_total"`
	BytesDone  int64   `json:"bytes_done"`
	Percent    float64 `json:"percent"`
	Error      string  `json:"error,omitempty"`
}

// UISettings 的语言同时用于平台提供的本地化标题，展示数量不改变任务范围。
type UISettings struct {
	Language        string `json:"language"`
	RecentDownloads int    `json:"recent_downloads"`
	HistoryPageSize int    `json:"history_page_size"`
	TaskPageSize    int    `json:"task_page_size"`
	TaskLogLines    int    `json:"task_log_lines"`
}

// DebugSettings 控制测试批次，0 表示不限条目。
type DebugSettings struct {
	TestItemLimit int `json:"test_item_limit"`
}

// ProducerSettings 只约束获取和解析请求，媒体传输使用独立消费者名额。
type ProducerSettings struct {
	WindowSeconds      int     `json:"window_seconds"`
	MaxRequests        int     `json:"max_requests"`
	MinIntervalSeconds float64 `json:"min_interval_seconds"`
	CooldownSeconds    float64 `json:"cooldown_seconds"`
}

// PipelineSettings 的队列容量不包含正在传输的媒体；平台时钟彼此独立。
type PipelineSettings struct {
	QueueCapacity                  int              `json:"queue_capacity"`
	Instagram                      ProducerSettings `json:"instagram"`
	YouTube                        ProducerSettings `json:"youtube"`
	InstagramSourceCacheTTLMinutes int              `json:"instagram_source_cache_ttl_minutes"`
	InstagramSourceCacheCapacity   int              `json:"instagram_source_cache_capacity"`
}

// Settings 中的并发上限只用于下载消费者。
type Settings struct {
	YouTubeConcurrent   int                   `json:"youtube_concurrent"`
	InstagramConcurrent int                   `json:"instagram_concurrent"`
	Template            string                `json:"template"`
	AutoArchive         bool                  `json:"auto_archive"`
	UseUploadDate       bool                  `json:"use_upload_date"`
	YouTube             YouTubeOptions        `json:"youtube"`
	UI                  UISettings            `json:"ui"`
	Debug               DebugSettings         `json:"debug"`
	Pipeline            PipelineSettings      `json:"pipeline"`
	Sleep               PlatformSleepSettings `json:"sleep"`
}

// ConfigurableEngine 在保存设置后更新共享调度器，不重启任务或复制 Cookie。
type ConfigurableEngine interface{ Configure(Settings) }

// MediaPreparer 在发现新媒体后、入队前准备可复用描述；解析失败仍由下载任务记录。
type MediaPreparer interface {
	Prepare(context.Context, Subscription, Content, YouTubeOptions, func(string)) error
}

// ScanResult 仅表示引擎在本次扫描中实际可获取的内容。
type ScanResult struct {
	StableID     string
	CurrentName  string
	Items        []Content
	MediaSources map[string]MediaSource // 引擎会话内复用，不写入内容索引或公开接口。
}

// MediaSource 是已解析媒体的临时下载描述，保留下载器需要的清单与请求头。
type MediaSource struct {
	URL      string         `json:"url"`
	Metadata map[string]any `json:"metadata"`
}

// Engine 把扫描及下载能力隔离，应用服务无需依赖命令行实现。
type Engine interface {
	Scan(context.Context, Subscription, int, func(string), func(ScanResult) error) (ScanResult, error)
	Download(context.Context, Subscription, Content, YouTubeOptions, string, func(string)) error
}

// ErrScanBoundary 表示增量已遇到扫描开始前的本地索引，属于正常完成。
var ErrScanBoundary = errors.New("增量扫描已到达本地缓存边界")

// IncrementalScanner 可把本地边界交给平台引擎，在继续分页前结束枚举。
// 普通 Engine 仍由服务层的逐条回调执行相同的停止规则。
type IncrementalScanner interface {
	ScanIncremental(context.Context, Subscription, int, []string, func(string), func(ScanResult) error) (ScanResult, error)
}

// NameResolver 只读取作者元数据，不创建订阅或下载任务。
type NameResolver interface {
	ResolveName(context.Context, Subscription) (string, error)
}

// AuthorResolver 在创建时取得稳定作者身份，不枚举或排队媒体内容。
type AuthorResolver interface {
	ResolveAuthor(context.Context, Subscription) (ScanResult, error)
}

// AvatarDownloader 是媒体下载任务的可选附属能力，保存位置由应用服务指定。
type AvatarDownloader interface {
	UpdateAvatar(context.Context, Subscription, string, func(string)) error
}

// DryRunner 验证未保存配置的远端请求，不修改任务、产物或日志文件。
type DryRunner interface {
	DryRun(context.Context, Subscription, YouTubeOptions, int, func(string)) error
}

// DownloadHistoryRepository 隔离历史展示生命周期，不删除下载索引或文件。
type DownloadHistoryRepository interface {
	DownloadHistory() ([]Content, error)
	DownloadHistoryPage(offset, limit int) ([]Content, error)
	DownloadHistoryCount() (int, error)
	RecentDownloadHistory(int) ([]Content, error)
	ClearDownloadHistory() (int, error)
	ShowDownloadHistory(int64) error
}

// Repository 隔离持久化；不采用数据库外键或触发器。
type Repository interface {
	PostDownloadRows(context.Context, int64, int) ([]PostDownloadRow, error)
	Posts(int64) ([]PostTask, error)
	Post(int64, string) (PostTask, error)
	DownloadItems(int64) ([]DownloadItem, error)
	RegisterPost(int64, string, Content, YouTubeOptions) (PostTask, error)
	PlatformRuns() ([]PlatformRun, error)
	SavePlatformRun(PlatformRun) error
	DownloadHistoryRepository
	PaidVideoPolicy
	BlockPaidVideos(string, int64) error
	AllowPaidVideos(string) error
	Subscriptions() ([]Subscription, error)
	Subscription(int64) (Subscription, error)
	SaveSubscription(*Subscription) error
	DeleteSubscription(int64) error
	ReinitializeSubscription(int64) (Task, error)
	QueueFullContent(int64, Content) (bool, error)
	Contents() ([]Content, error)
	Content(int64) (Content, error)
	SaveContent(*Content) error
	Tasks() ([]Task, error)
	// 控制操作只读取目标订阅；批量取消在同一事务内更新任务与内容。
	SubscriptionTasks(int64) ([]Task, error)
	CancelSubscriptionTasks(int64) ([]int64, error)
	TaskGroupStats(int) ([]TaskGroupStats, error)
	TaskContentRows(int64, int) ([]TaskContentRow, error)
	TaskStatusCounts() (map[string]int, error)
	Task(int64) (Task, error)
	SaveTask(*Task) error
	Enqueue(int64, int64, string) (Task, error)
	AddContentAndTask(int64, Content) error
	Settings() (Settings, error)
	SaveSettings(Settings) error
}
