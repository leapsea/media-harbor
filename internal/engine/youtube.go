package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// downloadYouTube 在一个视频名额内同步运行主任务和附属项，结束后才交还调度器。
// 必要的格式解析不抓取评论，附属项共用解析文件，避免各自重新访问播放器接口。
func (c CLI) downloadYouTube(ctx context.Context, sub domain.Subscription, item domain.Content, options domain.YouTubeOptions, dir string, log func(string)) error {
	common := c.common(sub)
	metadataPath := filepath.Join(dir, ".metadata.json")
	metadataArgs := append(append([]string{}, common...), "--dump-single-json", "--no-playlist", "--skip-download", "--ignore-no-formats-error")
	if options.Video || options.ExtractAudio {
		metadataArgs = append(metadataArgs, "-f", options.Format)
	}
	if options.AutoSubtitles {
		metadataArgs = append(metadataArgs, "--write-auto-subs", "--sub-langs", "all,-.*")
	}
	log("主任务：解析媒体格式与发布日期；附属任务尚未启动")
	if sub.RequestLanguage != "" {
		log("平台标题语言：" + sub.RequestLanguage)
	}
	metadata, err := run(ctx, "yt-dlp", append(metadataArgs, "--", item.URL), log, true)
	if err != nil {
		return err
	}
	if err = os.WriteFile(metadataPath, metadata, 0644); err != nil {
		return err
	}
	if published := PublicationDate(dir); !published.IsZero() {
		log(fmt.Sprintf("媒体 %s：已解析发布日期 %s", item.RemoteID, published.Format("2006-01-02")))
	} else {
		log(fmt.Sprintf("媒体 %s：平台未提供发布日期", item.RemoteID))
	}
	// 详情地址可能没有扁平扫描付费标识，解析后再次决策，尚未请求媒体和附属项。
	var access struct {
		ChannelID    string            `json:"channel_id"`
		Availability string            `json:"availability"`
		Formats      []json.RawMessage `json:"formats"`
	}
	_ = json.Unmarshal(metadata, &access)
	if domain.IsPaidAvailability(access.Availability) {
		if c.PaidPolicy != nil && access.ChannelID != "" {
			failed, policyErr := c.PaidPolicy.PaidFailure(access.ChannelID)
			if policyErr != nil {
				return policyErr
			}
			if failed > 0 {
				return &domain.PaidVideoSkippedError{ChannelID: access.ChannelID, FailedTaskID: failed}
			}
		}
		if len(access.Formats) == 0 && (options.Video || options.ExtraAudio || options.ExtractAudio) {
			return fmt.Errorf("YouTube 付费视频不可下载：平台未提供媒体格式")
		}
	}
	transfer(log, domain.TransferProgress{Kind: "video", Status: "queued", Title: PublicationTitle(dir), BytesTotal: youtubeTransferSize(metadata, "video")})
	// load-info-json 只读取本次媒体对象，下载格式仍由各阶段的选项决定。
	cached := append(append([]string{}, common...), "--load-info-json", metadataPath)
	// 403 需要重新取得媒体地址，普通 HTTP 重试不会刷新签名。
	media := func(stage string, stageArgs []string) error {
		kind := map[string]string{"主视频": "video", "字幕": "subtitles", "缩略图": "thumbnail", "额外音频": "audio"}[stage]
		total := youtubeTransferSize(metadata, kind)
		for attempt := 1; attempt <= config.YouTubeMediaAttempts; attempt++ {
			_, err := run(ctx, "yt-dlp", append(append([]string{}, cached...), stageArgs...), transferLog(log, kind, total), false)
			if err == nil {
				return nil
			}
			if ctx.Err() != nil || !strings.Contains(err.Error(), "HTTP Error 403") || attempt == config.YouTubeMediaAttempts {
				return youtubeDownloadError(stage, err)
			}
			log(fmt.Sprintf("%s：媒体请求 403；重新解析地址后进行第 %d 次尝试", stage, attempt+1))
			fresh, resolveErr := run(ctx, "yt-dlp", append(append([]string{}, metadataArgs...), "--", item.URL), log, true)
			if resolveErr != nil {
				return resolveErr
			}
			metadata = fresh
			if writeErr := os.WriteFile(metadataPath, fresh, 0644); writeErr != nil {
				return writeErr
			}
		}
		return nil
	}
	inventory := Inventory(item, dir)
	main := domain.YouTubeOptions{Video: (options.Video || (options.ExtractAudio && inventory.Audio(options.AudioFormat, true) == "")) && inventory.Video() == "", Format: options.Format, Fragments: options.Fragments}
	if main.Video {
		transfer(log, domain.TransferProgress{Kind: "video", Status: "running", BytesTotal: youtubeTransferSize(metadata, "video")})
		log("主任务：开始下载视频；附属任务等待主视频完成")
		if err = media("主视频", youtubeArgs(main, dir)); err != nil {
			transfer(log, domain.TransferProgress{Kind: "video", Status: "failed", Error: err.Error()})
			return err
		}
	} else if inventory.Video() != "" {
		log("主任务：复用已下载视频：" + inventory.Video())
	}
	if options.Video || options.ExtractAudio {
		transfer(log, completedTransfer("video", Inventory(item, dir)))
	}
	log("主任务已开始处理；依次执行已开启的附属任务，全部结束后释放视频名额")
	var failures []error
	child := func(name string, execute func() error) {
		if ctx.Err() != nil {
			return
		}
		log("附属任务 [" + name + "]：开始")
		kind := "metadata"
		switch {
		case strings.HasPrefix(name, "字幕"):
			kind = "subtitles"
		case name == "缩略图":
			kind = "thumbnail"
		case name == "额外音频":
			kind = "audio"
		case name == "本地音频提取":
			kind = "extracted"
		}
		transfer(log, domain.TransferProgress{Kind: kind, Status: "running", BytesTotal: youtubeTransferSize(metadata, kind)})
		if err := execute(); err != nil {
			failures = append(failures, fmt.Errorf("%s：%w", name, err))
			log("附属任务 [" + name + "]：失败；" + err.Error())
			transfer(log, domain.TransferProgress{Kind: kind, Status: "failed", Error: err.Error()})
		} else {
			log("附属任务 [" + name + "]：完成")
			transfer(log, completedTransfer(kind, Inventory(item, dir)))
		}
	}
	// 评论需要实时分页获取；只有主任务开始后才请求，并覆盖可变元数据。
	if options.Metadata || options.Comments {
		child("元数据与评论", func() error {
			if !options.Comments {
				args := append(append([]string{}, cached...), "--skip-download", "--write-info-json", "--force-overwrites", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"))
				_, err := run(ctx, "yt-dlp", args, log, false)
				return err
			}
			args := append(append([]string{}, metadataArgs...), "--write-info-json", "--force-overwrites", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"))
			if options.Comments {
				args = append(args, "--write-comments")
			}
			updated, err := run(ctx, "yt-dlp", append(args, "--", item.URL), log, true)
			if err != nil {
				return err
			}
			// 之后的附属项使用更新后的清单，字幕只选一份。
			metadata = updated
			return os.WriteFile(metadataPath, metadata, 0644)
		})
	}
	if options.Subtitles || options.AutoSubtitles {
		language := Inventory(item, dir).missingSubtitles(options, metadata)
		if language != "" {
			child("字幕 "+language, func() error {
				subs := domain.YouTubeOptions{Subtitles: options.Subtitles, AutoSubtitles: options.AutoSubtitles, SubtitleLanguages: language, Fragments: options.Fragments}
				return media("字幕", youtubeArgs(subs, dir))
			})
		} else {
			log("附属任务 [字幕]：已有符合条件的字幕或平台无匹配字幕，无需下载")
			p := completedTransfer("subtitles", Inventory(item, dir))
			if p.BytesTotal == 0 {
				p.Status = "skipped"
			}
			transfer(log, p)
		}
	}
	if options.Thumbnail && !Inventory(item, dir).Thumbnail() {
		child("缩略图", func() error {
			return media("缩略图", youtubeArgs(domain.YouTubeOptions{Thumbnail: true, Fragments: options.Fragments}, dir))
		})
	}
	if options.ExtraAudio && Inventory(item, dir).Audio(options.AudioFormat, false) == "" {
		child("额外音频", func() error {
			args := []string{"--no-playlist", "-f", "bestaudio/best", "-x", "--audio-format", options.AudioFormat, "--audio-quality", options.AudioQuality, "-o", filepath.Join(dir, "%(title)s-%(id)s.audio.%(ext)s")}
			return media("额外音频", args)
		})
	}
	if options.ExtractAudio && Inventory(item, dir).Audio(options.AudioFormat, true) == "" {
		child("本地音频提取", func() error {
			video := Inventory(item, dir).Video()
			if video == "" {
				return fmt.Errorf("本地提取音频需要已下载的视频")
			}
			out := filepath.Join(dir, trimVideoName(video)+".extracted."+options.AudioFormat)
			args := []string{"-y", "-i", video, "-vn"}
			if options.AudioFormat == "m4a" {
				args = append(args, "-c:a", "aac")
			}
			if options.AudioFormat == "m4a" || options.AudioFormat == "mp3" {
				if options.AudioQuality == "0" {
					quality := "0"
					if options.AudioFormat == "m4a" {
						quality = config.AACBestQuality
					}
					args = append(args, "-q:a", quality)
				} else {
					args = append(args, "-b:a", options.AudioQuality)
				}
			}
			_, err := run(ctx, "ffmpeg", append(args, out), log, false)
			return err
		})
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 已有产物同样提供实际完成状态；失败或跳过事件不被覆盖。
	for _, kind := range []string{"thumbnail", "audio", "extracted"} {
		p := completedTransfer(kind, Inventory(item, dir))
		if p.BytesTotal > 0 {
			transfer(log, p)
		}
	}
	log(fmt.Sprintf("主任务：附属任务全部结束；失败 %d 项；释放视频名额", len(failures)))
	return errors.Join(failures...)
}

// youtubeTransferSize 只使用引擎提供的大小，无法确认时保持未知。
func youtubeTransferSize(raw []byte, kind string) int64 {
	type format struct {
		Size   int64   `json:"filesize"`
		Approx float64 `json:"filesize_approx"`
		Vcodec string  `json:"vcodec"`
	}
	var info struct {
		Formats   []format `json:"formats"`
		Requested []format `json:"requested_formats"`
		Size      int64    `json:"filesize"`
		Approx    float64  `json:"filesize_approx"`
	}
	_ = json.Unmarshal(raw, &info)
	size := func(f format) int64 {
		if f.Size > 0 {
			return f.Size
		}
		return int64(f.Approx)
	}
	if kind == "video" {
		var total int64
		for _, f := range info.Requested {
			total += size(f)
		}
		if total == 0 {
			total = info.Size
			if total == 0 {
				total = int64(info.Approx)
			}
		}
		return total
	}
	if kind == "audio" {
		for i := len(info.Formats) - 1; i >= 0; i-- {
			if info.Formats[i].Vcodec == "none" {
				return size(info.Formats[i])
			}
		}
	}
	return 0
}

func completedTransfer(kind string, inventory ArtifactInventory) domain.TransferProgress {
	var size int64
	for _, path := range inventory.Paths {
		match := false
		ext := strings.ToLower(filepath.Ext(path))
		switch kind {
		case "video":
			match = path == inventory.Video()
		case "media":
			match = path == inventory.Video() || slices.Contains([]string{".jpg", ".jpeg", ".png", ".webp", ".gif"}, ext)
		case "audio":
			match = strings.Contains(path, ".audio.")
		case "extracted":
			match = strings.Contains(path, ".extracted.")
		case "metadata":
			match = strings.HasSuffix(path, ".info.json")
		case "thumbnail":
			match = slices.Contains([]string{".jpg", ".jpeg", ".png", ".webp"}, ext)
		case "subtitles":
			match = slices.Contains([]string{".vtt", ".srt", ".ass", ".ttml", ".srv1", ".srv2", ".srv3", ".json3"}, ext)
		}
		if match {
			if info, err := os.Stat(path); err == nil {
				size += info.Size()
			}
		}
	}
	status := "completed"
	if size == 0 {
		status = "skipped"
	}
	return domain.TransferProgress{Kind: kind, Status: status, BytesTotal: size, BytesDone: size, Percent: 100}
}

// ArtifactProgress 让详情页按真实产物确定大小和完成状态，不推测不存在的文件。
func ArtifactProgress(kind string, item domain.Content) domain.TransferProgress {
	return completedTransfer(kind, Inventory(item, ""))
}

func trimVideoName(path string) string {
	name := filepath.Base(path)
	return name[:len(name)-len(filepath.Ext(name))]
}
