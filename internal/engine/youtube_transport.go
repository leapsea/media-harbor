package engine

import (
	"context"
	_ "embed"
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
	if p := c.pipeline("youtube"); p != nil {
		release, err := p.acquireProducer(ctx, log)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return c.runYouTube(ctx, args, log, capture, "metadata")
}
