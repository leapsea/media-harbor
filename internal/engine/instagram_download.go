package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed instagram_cached.py
var instagramCached string

// downloadInstagram 复用临时媒体描述，签名地址失效时只刷新一次。
// 429 继续遵循原引擎等待策略，不通过重新解析放大请求。
func (c CLI) downloadInstagram(ctx context.Context, sub domain.Subscription, item domain.Content, dir string, inventory ArtifactInventory, log func(string)) error {
	// 已完成文件优先复用，避免为了取得媒体地址再次访问平台。
	if inventory.Video() != "" || inventory.Thumbnail() {
		log("复用已下载的 Instagram 媒体")
		transfer(log, completedTransfer("media", inventory))
		return nil
	}
	phase := func(value string) {
		transfer(log, domain.TransferProgress{Kind: "media", Status: "running", Phase: value})
	}
	// 尝试上限只覆盖明确失效的签名地址，普通错误交由任务状态报告。
	for attempt := 0; attempt < config.InstagramMediaAttempts; attempt++ {
		args := c.common(sub)
		// 原 Cookie、文件名与资源过滤规则由同一 CLI 配置生成。
		args = append(args, "-D", dir, "-f", "{post_id}_{num}.{extension}", "--filter", "num == "+strconv.Itoa(item.Asset), "--", item.URL)
		// 历史实现用 instagramProgress 包装全部进度，现在由 Go 解释 SDK 原始字节事件。
		script := "import sys, gallery_dl\nsys.exit(gallery_dl.main())\n"
		var source domain.MediaSource
		if c.instagram != nil {
			phase("解析或等待同帖媒体地址")
			var err error
			source, err = c.instagramSource(ctx, sub, item, instagramPhaseLog(log, phase))
			// 解析失败时结束本任务，不把失败结果交给媒体下载器重复请求。
			if err != nil {
				transfer(log, domain.TransferProgress{Kind: "media", Status: "failed", Error: err.Error()})
				return err
			}
			// 描述只写入任务工作目录，隐藏文件不会归档或成为下载产物。
			sourcePath := filepath.Join(dir, ".instagram-source.json")
			raw, err := json.Marshal(struct {
				domain.MediaSource
				PostURL string `json:"post_url"`
			}{source, item.URL})
			if err != nil {
				return err
			}
			if err = os.WriteFile(sourcePath, raw, 0600); err != nil {
				return err
			}
			args = append([]string{sourcePath}, args...)
			script = instagramCached
		}
		phase("连接媒体服务器，等待响应")
		// 准备阶段与实际收到媒体字节分别记录，零进度能说明当前等待原因。
		log(fmt.Sprintf("Instagram 媒体下载阶段开始；资源 %s；尝试 %d/%d", item.RemoteID, attempt+1, config.InstagramMediaAttempts))
		var output strings.Builder
		var outputMu sync.Mutex
		rejected := false
		progressOutput := transferLog(log, "media", 0)
		args = append([]string{"-u", "-c", instagramScript(script)}, args...)
		outputLog := func(message string) {
			// 两个输出流共享文本和阶段记录，避免竞争覆盖限流信息。
			outputMu.Lock()
			defer outputMu.Unlock()
			if strings.HasPrefix(message, "@instagram-source-rejected ") {
				rejected = true
				return
			}
			// DASH 的 yt-dlp 下载器不经过 HTTP 钩子，仍只识别明确的失效状态。
			if strings.Contains(message, "HTTP Error 403") || strings.Contains(message, "HTTP Error 404") || strings.Contains(message, "HTTP Error 410") {
				rejected = true
			}
			output.WriteString(message)
			instagramPhaseLog(progressOutput, phase)(message)
		}
		// 许可与日志属于当前媒体任务；解析任务使用同一个平台调度器。
		outputLog = instagramByteLog(outputLog)
		env, end, err := c.instagramController().begin(ctx, "media", outputLog)
		if err != nil {
			return err
		}
		release, err := c.consumer(ctx, "instagram", outputLog)
		if err != nil {
			end()
			return err
		}
		_, err = runWithEnv(ctx, "python3", args, outputLog, false, env)
		release()
		end()
		if err != nil && strings.Contains(output.String(), "redirect to login page") {
			err = fmt.Errorf("Instagram 下载需要登录：请在系统设置中编辑并保存有效 Cookie：%w", err)
		}
		// 只有收到明确失效状态且任务未取消时，才刷新一次同帖快照。
		if err != nil && rejected && c.instagram != nil && ctx.Err() == nil && attempt+1 < config.InstagramMediaAttempts {
			c.instagram.invalidate(item, source.URL)
			log("Instagram 媒体地址被拒绝，刷新帖子解析结果后再尝试一次；不重复重试旧签名地址")
			continue
		}
		if err != nil {
			transfer(log, domain.TransferProgress{Kind: "media", Status: "failed", Error: err.Error()})
		} else {
			transfer(log, completedTransfer("media", Inventory(item, dir)))
		}
		// 退出码来自真实下载结果，失败不能生成已完成或已跳过的进度。
		return err
	}
	return fmt.Errorf("Instagram 媒体下载尝试已结束")
}

// instagramByteLog 只由 Go 计算字节进度及限制刷新频率，内部事件不写入用户日志。
func instagramByteLog(log func(string)) func(string) {
	var last time.Time
	var mu sync.Mutex
	return func(line string) {
		if !strings.HasPrefix(line, "@instagram-bytes ") {
			log(line)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var progress struct{ Done, Total int64 }
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "@instagram-bytes ")), &progress) != nil {
			return
		}
		if time.Since(last) < config.DownloadLogInterval && progress.Done < progress.Total {
			return
		}
		last = time.Now()
		percent := 0.0
		if progress.Total > 0 {
			percent = min(99.9, float64(progress.Done)*100/float64(progress.Total))
		}
		transfer(log, domain.TransferProgress{Kind: "media", Status: "running", Phase: "传输媒体", BytesDone: progress.Done, BytesTotal: progress.Total, Percent: percent})
	}
}

// instagramPhaseLog 让限流等待进入任务快照，队列日志不再只报告 media 0%%。
func instagramPhaseLog(log func(string), phase func(string)) func(string) {
	return func(line string) {
		log(line)
		// 下载中的帖子解析也使用扫描入口，Go 的共享等待应进入该下载进度。
		if strings.HasPrefix(line, "Instagram 请求统一等待") {
			phase(line)
		}
		if strings.Contains(line, "429") && strings.Contains(line, "Waiting for") {
			phase("平台返回 429，请求限流等待：" + line)
			log("Instagram 请求被限流；当前任务等待平台允许重试并持续占用下载名额，尚未开始媒体传输")
		}
	}
}
