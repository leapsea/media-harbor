package service

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DryRun 只读取系统选项，把未保存配置交给可选的请求验证器。
// 不保存订阅、不入队、不创建工作目录，日志直接交付当前 HTTP 连接。
func (a *App) DryRun(ctx context.Context, sub domain.Subscription, log func(string)) error {
	address, err := url.Parse(strings.TrimSpace(sub.URL))
	if err != nil || address.Host == "" || (address.Scheme != "https" && address.Scheme != "http") {
		return fmt.Errorf("请输入有效任务 URL")
	}
	if sub.Platform != "youtube" && sub.Platform != "instagram" {
		return fmt.Errorf("平台应为 youtube 或 instagram")
	}
	if err := a.Credentials.Check(sub.Platform); err != nil {
		return err
	}
	runner, ok := a.Engine.(domain.DryRunner)
	if !ok {
		return fmt.Errorf("当前下载器不支持 dryrun")
	}
	settings, err := a.Settings()
	if err != nil {
		return err
	}
	options := settings.YouTube
	sub.RequestLanguage = settings.UI.Language
	// 当前表单的覆盖选项优先，未勾选覆盖时使用已有系统设置快照。
	if sub.YouTube != nil {
		options = *sub.YouTube
	}
	limit := settings.Debug.TestItemLimit
	// dryrun 本身是抽样请求验证，调试关闭后仍不枚举无限历史。
	if limit <= 0 {
		limit = config.DryRunItemLimit
	}
	sub.URL = address.String()
	started := time.Now()
	var mu sync.Mutex
	// 等待提示和引擎输出共享锁，保证每一行日志完整、按时间记录。
	output := func(message string) { mu.Lock(); defer mu.Unlock(); log(taskLogLine(time.Now(), message)) }
	output(fmt.Sprintf("dryrun 开始：平台 %s；最多验证 %d 个媒体条目；响应只读取到内存，媒体请求最多读取 %d 字节", sub.Platform, limit, config.DryRunProbeBytes))
	done, stopped := make(chan struct{}), make(chan struct{})
	// 平台请求等待时继续输出进度；连接关闭会停止计时和引擎执行。
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(config.ScanLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				output(fmt.Sprintf("请求验证进行中；已等待 %s", time.Since(started).Round(time.Second)))
			}
		}
	}()
	err = runner.DryRun(ctx, sub, options, limit, output)
	// 等计时协程退出后才发送最终结果，避免完成日志后出现等待提示。
	close(done)
	<-stopped
	if err != nil {
		output(fmt.Sprintf("dryrun 失败：%v；耗时 %s", err, time.Since(started).Round(time.Millisecond)))
		return err
	}
	output(fmt.Sprintf("dryrun 通过：请求流程可用，未保存任务或文件；耗时 %s", time.Since(started).Round(time.Millisecond)))
	return nil
}
