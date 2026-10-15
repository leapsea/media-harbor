package engine

import (
	"context"
	_ "embed"
	"fmt"
	"time"
)

// youtubeTransport 不包含业务选择逻辑，只适配原 SDK 请求和输出协议。
//
//go:embed youtube_transport.py
var youtubeTransport string

// 共用本机控制协议，生产算法在 Go 中，不把 Cookie 或平台响应持久化到脚本。
var youtubeScript = instagramTransport + "\n" + youtubeTransport

func (c CLI) runYouTube(ctx context.Context, args []string, log func(string), capture bool, stage string) ([]byte, error) {
	p := c.pipeline("youtube")
	if p == nil {
		return run(ctx, "yt-dlp", args, log, capture)
	}
	env, end, err := p.requests.begin(ctx, stage, log)
	if err != nil {
		return nil, err
	}
	defer end()
	if stage == "media" {
		release, err := c.consumer(ctx, "youtube", log)
		// 历史资产：每个 SDK 阶段单独调用 p.acquireConsumer(ctx, log)。
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return runWithEnv(ctx, "python3", append([]string{"-u", "-c", youtubeScript}, args...), log, capture, env)
}

// 作者和评论等旁路解析也排入同一生产者，不受下载并发数影响。
func (c CLI) runYouTubeProducer(ctx context.Context, args []string, log func(string), capture bool) ([]byte, error) {
	return c.runYouTubeProducerTimeout(ctx, args, log, capture, 0)
}

// 评论时限从取得生产者名额后开始，排队时间不挤占抓取预算；取消只结束本次 SDK。
func (c CLI) runYouTubeProducerTimeout(ctx context.Context, args []string, log func(string), capture bool, timeout time.Duration) ([]byte, error) {
	if p := c.pipeline("youtube"); p != nil {
		release, err := p.acquireProducer(ctx, log)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if timeout <= 0 {
		return c.runYouTube(ctx, args, log, capture, "metadata")
	}
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := c.runYouTube(timed, args, log, capture, "comments")
	if err != nil && timed.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return raw, fmt.Errorf("评论抓取超时（%s）；本次评论未完成，继续其他附属项：%w", timeout, context.DeadlineExceeded)
	}
	return raw, err
}
