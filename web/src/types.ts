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
 id: number; subscription_id: number; remote_id: string; url: string; title: string
 published: string; status: string; paths: string[]; tags: string[]; files: ContentFile[]; archived: boolean; error: string
}

/**
 * 挂载与权限是两个独立状态，不由目录是否存在推断。
 */
export interface Directory {
 name: string; path: string; mounted: string; writable: boolean; error: string
 checked_at: string; uid: number; gid: number
}
/** 展示参数单独建模，与下载行为分离。 */
export interface UISettings { language: string; recent_downloads: number; history_page_size: number; task_page_size: number }
export interface Settings {
 youtube_concurrent: number; instagram_concurrent: number
 template: string; auto_archive: boolean; use_upload_date: boolean; youtube: YouTubeOptions; ui: UISettings; debug: { test_item_limit: number }
}
export interface Dashboard {
 version: string; language: string; subscriptions: number; subscription_names: Record<number, string>
 counts: Record<string, number>
 recent: Content[]; directories: Directory[]
}

/** 订阅队列汇总；下载数量与扫描状态分别表示，测试上限由服务端返回。 */
export interface TaskGroup {
 subscription_id: number; status: string; scan_status: string
 counts: Record<string, number>; total: number; error: string
 updated_at: string; test_item_limit: number
}

/** 进度来自下载器实际传输和产物大小，未知大小保持为零。 */
export interface DetailPart {
 kind: string; status: string; bytes_total: number; bytes_done: number; percent: number
 error?: string; task_id: number; labels: string[]; number?: number
}
export interface DetailContent { key: string; title: string; status: string; parts: DetailPart[]; error?: string }
export interface TaskDetails { platform: string; items: DetailContent[] }
