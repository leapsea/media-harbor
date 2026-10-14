package engine

import (
	"context"
	"fmt"
	"media-harbor/internal/logtext"
	"time"
)

// requestLogMediaKey 仅关联日志中的媒体身份，不参与缓存、下载或平台许可。
type requestLogMediaKey struct{}

// requestTiming 只记录许可的时间，不保存URL、Cookie或响应内容。
type requestTiming struct {
	started time.Time
}

// requestLogStats 在平台控制锁内更新，一次SDK执行结束后输出单条统计。
// 响应累计只计算收到响应头的时间，不冒充媒体传输耗时。
type requestLogStats struct {
	count, success, failed, redirected int
	waited, response                   time.Duration
}

func (s requestLogStats) message(platform, stage, media string, unfinished int) string {
	message := fmt.Sprintf("[请求汇总] %s · %s · 请求 %d 次 · 成功 %d / 异常 %d · 等待累计 %s · 响应累计 %s", platform, logtext.Stage(stage), s.count, s.success, s.failed, logtext.Duration(s.waited), logtext.Duration(s.response))
	if media != "" {
		message = fmt.Sprintf("[请求汇总] %s · %s · 媒体 %s · 请求 %d 次 · 成功 %d / 异常 %d · 等待累计 %s · 响应累计 %s", platform, logtext.Stage(stage), media, s.count, s.success, s.failed, logtext.Duration(s.waited), logtext.Duration(s.response))
	}
	if s.redirected > 0 {
		message += fmt.Sprintf(" · 重定向 %d 次", s.redirected)
	}
	if unfinished > 0 {
		message += fmt.Sprintf(" · 未收到响应 %d 次", unfinished)
	}
	return message
}

// withRequestLogMedia 使同一个扫描任务中的各视频汇总可以直接溯源。
func withRequestLogMedia(ctx context.Context, media string) context.Context {
	return context.WithValue(ctx, requestLogMediaKey{}, media)
}
