// Package logtext 提供任务日志共用的时间与阶段名称，不改变调度或请求策略。
package logtext

import (
	"fmt"
	"time"
)

// Duration 保留短请求的毫秒精度，长等待使用中文单位，不显示内部 Go 时间格式。
func Duration(d time.Duration) string {
	if d <= 0 {
		return "0秒"
	}
	if d < time.Second {
		return fmt.Sprintf("%d毫秒", max(1, d.Round(time.Millisecond).Milliseconds()))
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1f秒", d.Seconds())
	}
	seconds := int(d.Round(time.Second) / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d小时%d分%d秒", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d分%d秒", seconds/60, seconds%60)
}

// Stage 只翻译已知执行阶段；未知阶段原样保留，避免丢失诊断上下文。
func Stage(stage string) string {
	if label, ok := map[string]string{"scan": "采集解析", "metadata": "视频信息解析", "media": "媒体下载", "avatar": "头像下载", "author": "作者识别", "author-page": "作者网页", "dryrun": "试运行"}[stage]; ok {
		return label
	}
	return stage
}
