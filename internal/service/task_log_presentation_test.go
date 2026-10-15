package service

import (
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"strings"
	"testing"
	"time"
)

// 重复语言警告合并，真实解析回退和错误不能随之被吞掉。
func TestTaskLogWarningPresentation(t *testing.T) {
	now := time.Now()
	p := newTaskLogPresentation(now)
	warning := `WARNING: [youtube] Preferring "zh-CN" translated fields. Note that some metadata extraction may fail or be incorrect.`
	if message := p.message(warning, now); !strings.Contains(message, "zh-CN") || !strings.Contains(message, "部分字段") {
		t.Fatal(message)
	}
	for range 3 {
		if p.message(warning, now) != "" {
			t.Fatal("重复提示仍逐条输出")
		}
	}
	if p.message(strings.Replace(warning, "[youtube]", "[youtube:tab]", 1), now) != "" {
		t.Fatal("频道和视频重复的语言提示未合并")
	}
	if summary := p.repeatedWarnings(); len(summary) != 1 || !strings.Contains(summary[0], "重复 4 次") {
		t.Fatal(summary)
	}
	for _, raw := range []string{"ERROR: HTTP Error 429: Too Many Requests", "WARNING: HTTP Error 403: Forbidden", "WARNING: [youtube] Incomplete data received in embedded initial data; re-fetching using API."} {
		for range 2 {
			if p.message(raw, now) == "" {
				t.Fatal("异常诊断被警告去重抑制", raw)
			}
		}
	}
}

// 受限格式提示可读且可溯源，不合并不同媒体的异常，也不丢失未知诊断。
func TestYouTubeRestrictedFormatWarnings(t *testing.T) {
	p := newTaskLogPresentation(time.Now())
	for raw, hint := range map[string]string{
		`WARNING: [youtube] Unable to fetch GVS PO Token for web client: Missing required Visitor Data. You may need to pass Visitor Data with --extractor-args "youtube:visitor_data=XXX"`: "访客标识",
		"WARNING: [youtube] video-id: Sign in to confirm you’re not a bot":                "机器人验证",
		"WARNING: [youtube] 请登录，以便我们确认你不是聊天机器人":                                           "机器人验证",
		"WARNING: Only images are available for download. use --list-formats to see them": "没有可下载的音视频格式",
	} {
		for range 2 {
			message := p.message(raw, time.Now())
			if !strings.Contains(message, hint) || !strings.Contains(message, strings.TrimPrefix(raw, "WARNING: ")) {
				t.Fatal(message)
			}
		}
	}
	unknown := "WARNING: [youtube] unexpected extraction diagnostic"
	if p.message(unknown, time.Now()) != "[引擎警告] [youtube] unexpected extraction diagnostic" {
		t.Fatal("未知警告未保留")
	}
}

// 同任务的倒计时即使数字和阶段变化也只出现一次，真实异常和采集数量仍输出。
func TestTaskLogCoalescesRequestInterval(t *testing.T) {
	p := newTaskLogPresentation(time.Now())
	for i, raw := range []string{
		"[请求等待] YouTube · 视频信息解析 · 请求间隔 · 约 2.8秒 后继续",
		"[请求等待] YouTube · 视频信息解析 · 请求间隔 · 约 2.7秒 后继续",
		"[请求等待] YouTube · 评论抓取 · 请求间隔 · 约 2.8秒 后继续",
	} {
		message := p.message(raw, time.Now())
		if (message != "") != (i == 0) {
			t.Fatal(i, message)
		}
	}
	for _, raw := range []string{"[请求等待] YouTube · 评论抓取 · 平台 429 统一冷却 · 约 1分 后继续", "[评论进度] YouTube · 媒体 id · 已采集 10/100 条", "[评论进度] YouTube · 媒体 id · 已采集 20/100 条"} {
		if p.message(raw, time.Now()) != raw {
			t.Fatal(raw)
		}
	}
}

// 模拟等待日志持续到来，只有真正的静默才补充扫描进度，无需等待真实十五秒。
func TestScanHeartbeatDoesNotRepeatRequestWait(t *testing.T) {
	start := time.Now()
	p := newTaskLogPresentation(start)
	p.progress(8, 2, 6)
	p.message("[请求等待] YouTube · 视频信息解析 · 请求间隔", start.Add(10*time.Second))
	if p.idleMessage(start.Add(20*time.Second), start) != "" {
		t.Fatal("正常请求等待被误报为平台等待")
	}
	message := p.idleMessage(start.Add(10*time.Second+config.ScanLogIdleInterval), start)
	if !strings.Contains(message, "已发现 8 / 新增 2 / 已存在 6") {
		t.Fatal(message)
	}
}

// 单视频省略没有意义的轮播序号与未知日期；真实轮播序号、地址和标题仍可检索。
func TestScanItemLogIdentifiesVideoAndCarousel(t *testing.T) {
	item := domain.Content{RemoteID: "video", Title: "标题", URL: "https://example.test/video"}
	message := scanItemLog("youtube", item, 0, 0, 1, 0, 0, 8, 0, 8)
	if strings.Contains(message, "资源序号") || strings.Contains(message, "待解析") || !strings.Contains(message, "已完成，跳过下载") || !strings.Contains(message, "\n地址：") {
		t.Fatal(message)
	}
	item.Asset = 2
	message = scanItemLog("instagram", item, 1, 0, 0, 0, 0, 2, 2, 0)
	if !strings.Contains(message, "资源序号 2") || !strings.Contains(message, "帖子资源") {
		t.Fatal(message)
	}
	item.Asset = 0
	message = mediaItemLog("youtube", item, "开始下载")
	if strings.Contains(message, "资源序号") || strings.Contains(message, "待解析") || !strings.Contains(message, "[视频] video · 开始下载") {
		t.Fatal(message)
	}
}
