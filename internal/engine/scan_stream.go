package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"media-harbor/internal/domain"
	"net/url"
	"os/exec"
	"time"
)

// instagramStream 共用已安装的 gallery-dl，递归解析不缓存整批历史。
//
//go:embed instagram_stream.py
var instagramStream string

// scanStream 同步交付每条媒体，仓储入队后才继续读取，形成自然背压。
// 元数据与错误流分离，部分结果不因后续分页失败而撤销。
func scanStream(ctx context.Context, name string, args []string, sub domain.Subscription, log func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
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
			parseErr = deliveryErr
			cancel()
			break
		}
		if err != nil {
			parseErr = err
			cancel()
			break
		}
	}
	waitErr := cmd.Wait()
	stderr.flush()
	if parseErr != nil {
		return result, parseErr
	}
	if waitErr != nil {
		return result, fmt.Errorf("%s 执行失败：%w", name, waitErr)
	}
	return result, nil
}
