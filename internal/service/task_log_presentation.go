package service

import (
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/logtext"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 频道列表使用 youtube:tab，视频使用 youtube；两者的语言警告语义相同。
var translatedFieldsWarning = regexp.MustCompile(`^WARNING: \[youtube(?::[^\]]+)?\] Preferring "([^"]+)" translated fields\.`)

// taskLogPresentation 属于一次执行，警告去重不跨任务，也不影响原始错误与认证判断。
// 引擎回调和扫描进度协程共用一个锁，进度不会读取正在修改的统计变量。
type taskLogPresentation struct {
	mu                           sync.Mutex
	lastActivity                 time.Time
	warnings                     map[string]int
	order                        []string
	discovered, added, duplicate int
}

func newTaskLogPresentation(at time.Time) *taskLogPresentation {
	return &taskLogPresentation{lastActivity: at, warnings: map[string]int{}}
}

// message 只合并相同警告；未知警告和所有错误保持原文，首次提示始终显示。
func (p *taskLogPresentation) message(raw string, at time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastActivity = at
	if !strings.HasPrefix(raw, "WARNING:") {
		return raw
	}
	message := "[引擎警告] " + strings.TrimSpace(strings.TrimPrefix(raw, "WARNING:"))
	coalescible := false
	if match := translatedFieldsWarning.FindStringSubmatch(raw); match != nil {
		message = fmt.Sprintf("[引擎警告] YouTube 使用 %s 翻译的元数据，部分字段可能缺失或不准确", match[1])
		coalescible = true
	} else if strings.HasPrefix(raw, "WARNING: [youtube] unable to extract yt initial data;") {
		message = "[引擎警告] 无法解析 YouTube 网页初始数据"
	} else if raw == "WARNING: [youtube] Incomplete data received in embedded initial data; re-fetching using API." {
		message = "[引擎警告] YouTube 网页数据不完整，正在通过 API 重新获取"
	} else {
		// 仅解释已知的 YouTube 限制，保留原文供排查；不据此修改认证或请求参数。
		// 仅图片警告来自 yt-dlp 通用层，尚不能单独判断是限流、验证还是内容限制。
		hint := ""
		switch {
		case strings.HasPrefix(raw, "WARNING: [youtube]") && strings.Contains(raw, "Unable to fetch GVS PO Token") && strings.Contains(raw, "Missing required Visitor Data"):
			hint = "YouTube 网页未提供访客标识，当前客户端无法获取媒体校验令牌，部分格式可能不可用"
		case strings.HasPrefix(raw, "WARNING: [youtube]") && (strings.Contains(raw, "Sign in to confirm you’re not a bot") || strings.Contains(raw, "Sign in to confirm you're not a bot") || strings.Contains(raw, "请登录，以便我们确认你不是聊天机器人")):
			hint = "YouTube 要求机器人验证；当前解析可能缺少媒体格式，增加下载并发无法解决验证限制"
		case raw == "WARNING: Only images are available for download. use --list-formats to see them":
			hint = "本次解析仅返回图片或故事板，没有可下载的音视频格式；请结合前面的限流、验证或访问限制日志排查"
		}
		if hint != "" {
			message = "[引擎警告] " + hint + "；原始提示：" + strings.TrimSpace(strings.TrimPrefix(raw, "WARNING:"))
		}
	}
	// 只合并稳定的语言提示，接口异常、解析回退与未知警告仍逐条保留。
	if !coalescible {
		return message
	}
	if count, exists := p.warnings[message]; exists {
		p.warnings[message] = count + 1
		return ""
	}
	if len(p.warnings) < config.TaskWarningLimit {
		p.warnings[message] = 1
		p.order = append(p.order, message)
	}
	return message
}

// repeatedWarnings 在结束时保留重复数量，便于判断警告是否贯穿整个扫描。
func (p *taskLogPresentation) repeatedWarnings() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := []string{}
	for _, message := range p.order {
		if count := p.warnings[message]; count > 1 {
			result = append(result, fmt.Sprintf("[警告汇总] 同一提示重复 %d 次：%s", count-1, strings.TrimPrefix(message, "[引擎警告] ")))
		}
	}
	return result
}

func (p *taskLogPresentation) progress(discovered, added, duplicate int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discovered, p.added, p.duplicate = discovered, added, duplicate
}

// idleMessage 只在没有新日志时补充状态，不将主动请求间隔误报为平台卡住。
func (p *taskLogPresentation) idleMessage(now, started time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	idle := now.Sub(p.lastActivity)
	if idle < config.ScanLogIdleInterval {
		return ""
	}
	return fmt.Sprintf("[扫描进度] 最近 %s 暂无新进展 · 已发现 %d / 新增 %d / 已存在 %d · 已运行 %s", logtext.Duration(idle), p.discovered, p.added, p.duplicate, logtext.Duration(now.Sub(started)))
}

// scanItemLog 将条目状态、累计数量和标题地址分行，不为单视频显示轮播序号零。
func scanItemLog(platform string, item domain.Content, added, retried, completed, pending, skipped, discovered, totalAdded, totalDuplicate int) string {
	state := "已有记录，跳过重复入队"
	switch {
	case added > 0:
		state = "已加入下载队列"
	case retried > 0:
		state = "缺项已重新排队"
	case completed > 0:
		state = "已完成，跳过下载"
	case pending > 0:
		state = "已在队列，跳过重复入队"
	case skipped > 0:
		state = "已有记录，跳过重复入队"
	}
	details := fmt.Sprintf("%s · 累计发现 %d / 新增 %d / 已存在 %d", state, discovered, totalAdded, totalDuplicate)
	return mediaItemLog(platform, item, details)
}

// mediaItemLog 供生产者和下载器共用条目展示，避免进入下载阶段又出现序号零。
func mediaItemLog(platform string, item domain.Content, details string) string {
	label := "视频"
	if platform == "instagram" {
		label = "帖子资源"
	}
	message := fmt.Sprintf("[%s] %s · %s", label, item.RemoteID, details)
	if item.Asset > 0 {
		message += fmt.Sprintf(" · 资源序号 %d", item.Asset)
	}
	if !item.Published.IsZero() {
		message += " · 发布日期 " + publicationLabel(item.Published)
	}
	if item.Title != "" {
		message += "\n标题：" + item.Title
	}
	if item.URL != "" {
		message += "\n地址：" + item.URL
	}
	return message
}
