/**
 * 类型与 Go 的 JSON 接口保持一致。
 */
export interface YouTubeOptions {
 video: boolean; format: string; subtitles: boolean; auto_subtitles: boolean
 subtitle_languages: string; comments: boolean
 extra_audio: boolean; extract_audio: boolean; audio_format: string; audio_quality: string
 thumbnail: boolean; metadata: boolean; fragments: number
}

/**
 * 首次确定的目录名称保留，当前名称由平台扫描更新。
 */
export interface Subscription {
 id: number; platform: string; url: string; stable_id: string; mode: string
 display_name: string; current_name: string; enabled: boolean; schedule: string
 template: string; archive: boolean; youtube: YouTubeOptions | null
 last_scan: string; last_success: string; next_run: string; created_at: string
 avatar_url?: string
}

/** 一个文件可以同时承载评论和元数据，数量来自对应文件。 */
export interface ContentFile { path: string; tags: string[]; comment_count?: number }
export interface Content {
 id: number; post_id: number; expected_assets: number; subscription_id: number; remote_id: string; url: string; title: string
 published: string; status: string; paths: string[]; tags: string[]; files: ContentFile[]; archived: boolean; error: string
}
export interface ContentHistoryPage { items: Content[]; total: number; page: number; page_size: number }

/**
 * 挂载与权限是两个独立状态，不由目录是否存在推断。
 */
export interface Directory {
 name: string; path: string; mounted: string; writable: boolean; error: string
 checked_at: string; uid: number; gid: number
}
/** 展示参数单独建模，与下载行为分离。 */
export interface UISettings { language: string; recent_downloads: number; history_page_size: number; task_page_size: number; task_log_lines: number }
/** 每日休眠按配置时区解释，支持跨午夜。 */
export interface SleepSettings { enabled: boolean; start: string; end: string; timezone: string }
export interface Settings {
 /** 每个平台拥有独立休眠窗口，互不继承或影响。 */
 sleep: Record<'instagram' | 'youtube', SleepSettings>
	/** 解析请求预算与下载线程数独立，两个平台分别计时。 */
	pipeline: {
	 queue_capacity: number
	 instagram: ProducerSettings; youtube: ProducerSettings
	 instagram_source_cache_ttl_minutes: number; instagram_source_cache_capacity: number
	}
 youtube_concurrent: number; instagram_concurrent: number
 template: string; auto_archive: boolean; use_upload_date: boolean; youtube: YouTubeOptions; ui: UISettings; debug: { test_item_limit: number }
}

/** 滑动窗口统计真实接口请求，包括分页、重试和重定向。 */
export interface ProducerSettings {
 window_seconds: number; max_requests: number
 min_interval_seconds: number; cooldown_seconds: number
}
export interface Dashboard {
 version: string; language: string; subscriptions: number; subscription_names: Record<number, string>
 counts: Record<string, number>
 recent: Content[]; directories: Directory[]
}

/** 订阅队列汇总；下载数量与扫描状态分别表示，测试上限由服务端返回。 */
export interface TaskGroup {
 platform_disabled: boolean; blocked_reason?: string
 sleep_until?: string
 subscription_id: number; status: string; scan_status: string
 counts: Record<string, number>; total: number; error: string
 updated_at: string; test_item_limit: number
}

/** 进度来自下载器实际传输和产物大小，未知大小保持为零。 */
export interface DetailPart {
	/** 当前执行阶段可用于解释零字节进度和平台等待。 */
	phase?: string
 kind: string; status: string; bytes_total: number; bytes_done: number; percent: number
 error?: string; resource_id: number; task_id: number; labels: string[]; number?: number
}
export interface DetailContent { post_id: number; manifest_complete: boolean; expected: number; completed: number; key: string; title: string; status: string; parts: DetailPart[]; error?: string }
export interface TaskDetails { platform: string; items: DetailContent[] }

/** 配置存在、Cookie可用和平台能否运行分别展示。 */
export interface CookieStatus { configured: boolean; usable: boolean; platform_enabled: boolean; state: string; reason?: string; revision: number; disabled_at?: string }

/** 订阅配置保存平台、地址和下载选项；帖子任务保存整帖期望清单；下载资源任务保存资源与附属项的实际下载状态。 */
export interface PostTask { id: number; subscription_id: number; remote_id: string; url: string; title: string; status: string; kinds: string[]; manifest_complete: boolean; expected: number; completed: number; error?: string; updated_at: string }
export interface DownloadItem { id: number; post_id: number; content_id: number; required: boolean; kind: string; status: string; outcome?: string; error?: string; paths: string[]; updated_at: string }
