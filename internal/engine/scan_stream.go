package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"media-harbor/internal/domain"
	"net/url"
	"os"
	"os/exec"
	"time"
)

// instagramStream 共用已安装的 gallery-dl，递归解析不缓存整批历史。
//
//go:embed instagram_stream.py
var instagramStream string

// scanStream 同步交付每条媒体，仓储入队后才继续读取，形成自然背压。
// 元数据与错误流分离，部分结果不因后续分页失败而撤销。
func scanStream(ctx context.Context, name string, args []string, sub domain.Subscription, log func(string), emit func(domain.ScanResult) error, extraEnv ...[]string) (domain.ScanResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	for _, env := range extraEnv {
		cmd.Env = append(cmd.Env, env...)
	}
	// stdout 承载结构化媒体，stdin 仅承载读取确认，stderr 独立记录引擎错误。
	var input io.WriteCloser
	if sub.Platform == "instagram" {
		// 每条输出收到确认后才继续提取，首个已有媒体不会被后台历史请求越过。
		cmd.Env = append(cmd.Env, "MEDIA_HARBOR_STREAM_ACK=1")
		var err error
		input, err = cmd.StdinPipe()
		if err != nil {
			return domain.ScanResult{}, err
		}
		defer input.Close()
	}
	stderr := &logWriter{output: &commandLog{log: log, now: time.Now}}
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return domain.ScanResult{}, err
	}
	if err = cmd.Start(); err != nil {
		return domain.ScanResult{}, err
	}
	result := domain.ScanResult{}
	var parseErr error
	dec := json.NewDecoder(pipe)
	for {
		var raw json.RawMessage
		err = dec.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			parseErr = err
			cancel()
			break
		}
		var batch domain.ScanResult
		if sub.Platform == "youtube" {
			batch, err = parseYouTube(raw)
			// 视频行 ID 属于视频；主页身份使用 channel_id，列表保留原列表目标。
			var entry youtubeEntry
			_ = json.Unmarshal(raw, &entry)
			if domain.TaskMode(sub.Platform, sub.URL) == "profile" {
				batch.StableID = entry.ChannelID
				batch.CurrentName = entry.Channel
				if batch.CurrentName == "" {
					batch.CurrentName = entry.Uploader
				}
				target, _ := url.Parse(sub.URL)
				if target != nil && target.Query().Get("list") != "" {
					batch.StableID = entry.PlaylistID
					batch.CurrentName = entry.PlaylistTitle
				}
			}
		} else {
			batch, err = parseInstagram(append(append([]byte{'['}, raw...), ']'))
		}
		if batch.StableID != "" {
			result.StableID = batch.StableID
		}
		if batch.CurrentName != "" {
			result.CurrentName = batch.CurrentName
		}
		if deliveryErr := emit(batch); deliveryErr != nil {
			// 停止原因先保留，再杀掉等待确认的提取器，避免误报进程取消。
			parseErr = deliveryErr
			cancel()
			break
		}
		if err != nil {
			parseErr = err
			cancel()
			break
		}
		if input != nil {
			// 回调成功才允许提取下一条媒体，背压会自然传递到平台请求。
			// 最后一条输出后正常退出也可能关闭输入；最终退出状态由 Wait 核验。
			_, _ = io.WriteString(input, "continue\n")
		}
	}
	waitErr := cmd.Wait()
	// 确认进程退出后再刷新末尾诊断，正常边界由服务层判断。
	stderr.flush()
	if parseErr != nil {
		return result, parseErr
	}
	if waitErr != nil {
		return result, fmt.Errorf("%s 执行失败：%w", name, waitErr)
	}
	return result, nil
}
