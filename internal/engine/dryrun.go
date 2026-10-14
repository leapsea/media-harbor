package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// dryRunScript 使用只读 Cookie、内存缓存与内存响应探测，标准输出只传事件。
//
// 历史 dryrun.py 保留作为旧流程参考，正式运行只嵌入 SDK 适配。
//
//go:embed dryrun_sdk.py
var dryRunScript string

// DryRun 的请求与日志只在内存和管道中流转，永远不调用 Download 或仓储写接口。
// Go 继续维护字幕选择策略，Python 复用下载器的鉴权及请求实现。
func (c CLI) DryRun(ctx context.Context, sub domain.Subscription, options domain.YouTubeOptions, limit int, log func(string)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := c.pipeline(sub.Platform)
	var producerRelease func()
	if p != nil {
		var err error
		producerRelease, err = p.acquireProducer(ctx, log)
		if err != nil {
			return err
		}
		defer func() {
			if producerRelease != nil {
				producerRelease()
			}
		}()
	}
	// 脚本通过命令参数执行，不创建临时脚本；Python 导入也禁止生成字节码。
	script := dryRunScript
	if sub.Platform == "instagram" {
		script = instagramScript(script)
	} else if p != nil {
		// 共用 SDK HTTP 适配，但只读入口自己管理提取与输出，不运行 CLI main。
		script = instagramTransport + "\n" + strings.Replace(youtubeTransport, "yt_dlp.main(sys.argv[1:])", "", 1) + "\n" + script
	}
	cmd := exec.CommandContext(ctx, "python3", "-B", "-u", "-c", script)
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if sub.Platform == "instagram" || p != nil {
		controller := c.instagramController()
		if p != nil {
			controller = p.requests
		}
		env, end, err := controller.begin(ctx, "dryrun", log)
		if err != nil {
			return err
		}
		defer end()
		cmd.Env = append(cmd.Env, env...)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &logWriter{output: &commandLog{log: log, now: time.Now}}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	encoder := json.NewEncoder(input)
	// Cookie 路径仅供读取，回写策略由两个 SDK 的只读适配器明确关闭。
	cookie := ""
	if _, err := os.Stat(c.CookiePath(sub.Platform)); err == nil {
		cookie = c.CookiePath(sub.Platform)
	}
	if err = encoder.Encode(map[string]any{"subscription": sub, "options": options, "limit": limit,
		"language": sub.RequestLanguage,
		"cookie":   cookie, "gallery_config": filepath.Join(c.DataDir, "gallery-dl.json"),
		"user_agent": config.InstagramRequestUserAgent(), "headers": json.RawMessage(config.InstagramHeaders), "probe_bytes": config.DryRunProbeBytes, "manifest_depth": config.DryRunManifestDepth}); err != nil {
		cancel()
		_ = cmd.Wait()
		return err
	}
	decoder := json.NewDecoder(output)
	var failure error
	count := 0
	// 事件管道形成背压：收到条目后先完成字幕策略，再允许请求探测继续。
	// 元数据包含临时签名地址，只用于当前处理，不写入日志或磁盘。
	for decoder.More() {
		var event struct {
			Kind     string          `json:"kind"`
			Role     string          `json:"role"`
			Message  string          `json:"message"`
			Metadata json.RawMessage `json:"metadata"`
		}
		if err := decoder.Decode(&event); err != nil {
			failure = err
			cancel()
			break
		}
		switch event.Kind {
		case "item":
			// Go 选择要验证的媒体与字幕，并控制清单递归及错误语义。
			if event.Role != "avatar" {
				count++
			}
			probes, err := dryRunProbes(event.Metadata, sub.Platform, options, log)
			if err == nil {
				// 单条元数据已解析，内存探测交给下载消费者，扫描执行权暂时归还。
				if producerRelease != nil {
					producerRelease()
					producerRelease = nil
				}
				var consumerRelease func()
				consumerRelease, err = c.consumer(ctx, sub.Platform, log)
				if err == nil {
					for _, probe := range probes {
						if err = probeDryResponse(encoder, decoder, probe, 0, log); err != nil {
							break
						}
					}
					consumerRelease()
				}
			}
			if err != nil {
				failure = err
				cancel()
				break
			}
			if p != nil {
				producerRelease, err = p.acquireProducer(ctx, log)
				if err != nil {
					failure = err
					cancel()
					break
				}
			}
			if err = encoder.Encode(map[string]string{"kind": "next"}); err != nil {
				failure = err
				cancel()
			}
		case "error":
			failure = fmt.Errorf("%s", event.Message)
			log(event.Message)
		default:
			log(event.Message)
		}
	}
	waitErr := cmd.Wait()
	// 等待进程与 stderr 复制结束后再报告结果，保留最后一条引擎错误。
	stderr.flush()
	if failure != nil {
		return failure
	}
	if waitErr != nil {
		return fmt.Errorf("dryrun 执行失败：%w", waitErr)
	}
	if count == 0 {
		return fmt.Errorf("平台未返回可验证的媒体")
	}
	log(fmt.Sprintf("扫描阶段：发现 %d；新增 0；重复 0；未创建任务", count))
	if sub.Platform == "youtube" && (options.Video || options.ExtraAudio || options.ExtractAudio) {
		// 只验证后处理工具可启动，合并和提取要求文件，不能放入只读 dryrun。
		if _, err := run(ctx, "ffmpeg", []string{"-version"}, func(string) {}, false); err != nil {
			return fmt.Errorf("后处理引擎不可用：%w", err)
		}
		log("后处理阶段：FFmpeg 可用；文件合并、音频提取及归档未执行")
	}
	return nil
}
