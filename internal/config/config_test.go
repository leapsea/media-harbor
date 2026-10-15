package config

import "testing"

// TestInitialSettings 固定首次启动默认值，避免数据库重建后仍带有旧配置。
func TestInitialSettings(t *testing.T) {
	for _, key := range []string{"YTDLP_MAX_CONCURRENT", "GALLERY_DL_MAX_CONCURRENT"} {
		t.Setenv(key, "")
	}
	settings := Defaults()
	if !settings.UseUploadDate || settings.YouTubeConcurrent != 3 || settings.InstagramConcurrent != 6 || settings.Template != "{platform}/{directory_name}/{year}/{month}{day}/{filename}" {
		t.Fatalf("settings=%+v", settings)
	}
	if settings.YouTube.AudioFormat != "best" || settings.YouTube.AudioQuality != "0" || settings.YouTube.Fragments != 6 {
		t.Fatalf("youtube=%+v", settings.YouTube)
	}
	// 评论分页的主要成本是请求间隔；缩短间隔仍保持原有每分钟20次预算。
	youtube := settings.Pipeline.YouTube
	if youtube.WindowSeconds != 60 || youtube.MaxRequests != 20 || youtube.MinIntervalSeconds != 3 || youtube.CooldownSeconds != 180 {
		t.Fatalf("youtube pipeline=%+v", youtube)
	}
	// 初始化的均匀请求节奏应与窗口上限相容，避免预算和间隔相互抵消。
	if youtube.MinIntervalSeconds*float64(youtube.MaxRequests) != float64(youtube.WindowSeconds) {
		t.Fatalf("YouTube请求间隔与窗口预算不一致：%+v", youtube)
	}
}

// TestSavedDefaults 初始化恢复用户当前已确认的字幕、音频、缩略图和元数据选项。
func TestSavedDefaults(t *testing.T) {
	o := Defaults().YouTube
	if !o.Subtitles || !o.AutoSubtitles || !o.ExtraAudio || !o.Thumbnail || !o.Metadata || !o.Comments || o.ExtractAudio {
		t.Fatal(o)
	}
	if o.Format != "bestvideo+bestaudio/best" || o.SubtitleLanguages != "zh.*,en" {
		t.Fatal(o)
	}
}

// TestUISettingsDefaults 界面数量独立于引擎下载上限，初始化统一由配置快照提供。
func TestUISettingsDefaults(t *testing.T) {
	ui := Defaults().UI
	if ui.Language != "zh-CN" || ui.RecentDownloads != 3 || ui.HistoryPageSize != 20 || ui.TaskPageSize != 20 || ui.TaskLogLines != 100 {
		t.Fatal(ui)
	}
}
