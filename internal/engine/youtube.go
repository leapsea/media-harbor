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
	metadataArgs := c.youtubeMetadataArgs(sub, options)
	metadata, err := c.youtubeMetadata(ctx, sub, item, options, log, false)
	if err != nil {
		c.forgetYouTubeError(sub, item, options)
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
	// 403 或不可用的格式清单需要重新解析；普通 HTTP 重试不会刷新签名与格式。
	media := func(stage string, stageArgs []string) error {
		kind := map[string]string{"主视频": "video", "字幕": "subtitles", "缩略图": "thumbnail", "额外音频": "audio"}[stage]
		total := youtubeTransferSize(metadata, kind)
		for attempt := 1; attempt <= config.YouTubeMediaAttempts; attempt++ {
			_, err := c.runYouTube(ctx, append(append([]string{}, cached...), stageArgs...), transferLog(log, kind, total), false, "media")
			if err == nil {
				return nil
			}
			refreshFormat := strings.Contains(err.Error(), "Requested format is not available")
			if ctx.Err() != nil || (!strings.Contains(err.Error(), "HTTP Error 403") && !refreshFormat) || attempt == config.YouTubeMediaAttempts {
				return youtubeDownloadError(stage, err)
			}
			// 历史资产：仅 HTTP Error 403 会重新解析资源地址，格式不可用直接失败。
			if refreshFormat {
				log(fmt.Sprintf("%s：所选格式不可用；重新解析完整格式清单后重试，保持原画质设置", stage))
			} else {
				log(fmt.Sprintf("%s：媒体请求 403；重新解析资源地址后重试", stage))
			}
			fresh, resolveErr := c.youtubeMetadata(ctx, sub, item, options, log, true)
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
			writeMetadata := func() error {
				args := append(append([]string{}, cached...), "--skip-download", "--write-info-json", "--force-overwrites", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"))
				_, err := c.runYouTube(ctx, args, log, false, "media")
				return err
			}
			if !options.Comments {
				return writeMetadata()
			}
			args := append(append([]string{}, metadataArgs...), "--write-info-json", "--force-overwrites", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"))
			if options.Comments {
				args = append(args, "--write-comments")
			}
			updated, err := c.runYouTubeProducer(ctx, append(args, "--", item.URL), log, true)
			if err != nil {
				return err
			}
			// 评论独立保存；限流或验证失败产生的空媒体清单不能污染下载快照。
			if youtubeHasMediaFormats(metadata) && !youtubeHasMediaFormats(updated) {
				var snapshot, comments map[string]json.RawMessage
				if err := json.Unmarshal(metadata, &snapshot); err != nil {
					return err
				}
				if err := json.Unmarshal(updated, &comments); err != nil {
					return err
				}
				// 仅合并本次评论；不将受限解析的其他缺失字段覆盖到主任务信息。
				for _, key := range []string{"comments", "comment_count"} {
					if value, ok := comments[key]; ok {
						snapshot[key] = value
					}
				}
				metadata, err = json.Marshal(snapshot)
				if err != nil {
					return err
				}
				if err := os.WriteFile(metadataPath, metadata, 0644); err != nil {
					return err
				}
				// SDK 在没有选中格式时跳过 info.json，借有效快照补写，沿用其命名与清理规则。
				if err := writeMetadata(); err != nil {
					return err
				}
				log("评论解析结果已保存；本次解析未返回音视频格式，后续附属下载沿用主任务的有效清单；地址过期时仍重新解析")
				return nil
			}
			// 之后的附属项使用有效的更新清单，字幕只选一份。
			metadata = updated
			// 历史资产：评论解析结果直接覆盖 metadata 与 .metadata.json，未区分仅图片清单。
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
	if options.ExtraAudio && Inventory(item, dir).Audio("best", false) == "" {
		child("额外音频", func() error {
			// best 保留 Opus/AAC 等源编码；只有封装内含视频时才无损分离音轨。
			log("额外音频：下载平台最高品质音轨，保留原编码，不指定转码格式或码率")
			args := []string{"--no-playlist", "-f", "bestaudio/best", "-x", "--audio-format", "best", "-o", filepath.Join(dir, "%(title)s-%(id)s.audio.%(ext)s")}
			// 历史资产：--audio-format options.AudioFormat --audio-quality options.AudioQuality 强制转换源音轨。
			return media("额外音频", args)
		})
	}
	if options.ExtractAudio && Inventory(item, dir).Audio(options.AudioFormat, true) == "" {
		child("本地音频提取", func() error {
			video := Inventory(item, dir).Video()
			if video == "" {
				return fmt.Errorf("本地提取音频需要已下载的视频")
			}
			format := options.AudioFormat
			if format == "best" {
				var err error
				format, err = originalAudioExtension(ctx, video, log)
				if err != nil {
					return err
				}
			}
			out := filepath.Join(dir, trimVideoName(video)+".extracted."+format)
			args := []string{"-y", "-i", video, "-vn"}
			if options.AudioFormat == "best" {
				// 只复制第一条音轨的编码数据，不进入任何音频编码器。
				args = append(args, "-map", "0:a:0", "-c:a", "copy")
				log("本地音频提取：保留原编码，输出格式 " + format)
			} else if options.AudioFormat == "m4a" {
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

// originalAudioExtension 根据实际音轨选择容器，仅用于无损提取，不猜测视频扩展名。
// 未列出的编码使用支持多种音轨的 Matroska 音频容器，仍执行 stream copy。
func originalAudioExtension(ctx context.Context, video string, log func(string)) (string, error) {
	raw, err := run(ctx, "ffprobe", []string{"-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", video}, log, true)
	if err != nil {
		return "", fmt.Errorf("读取本地音轨失败：%w", err)
	}
	codec := strings.TrimSpace(string(raw))
	switch codec {
	case "":
		return "", fmt.Errorf("本地视频未包含可提取的音轨")
	case "aac", "alac":
		return "m4a", nil
	case "opus", "mp3", "flac", "ac3", "eac3":
		return codec, nil
	case "vorbis":
		return "ogg", nil
	default:
		return "mka", nil
	}
}

// youtubeMetadataArgs 不抓评论；同一份格式清单由主视频和附件复用。
func (c CLI) youtubeMetadataArgs(sub domain.Subscription, options domain.YouTubeOptions) []string {
	args := append(c.common(sub), "--dump-single-json", "--no-playlist", "--skip-download", "--ignore-no-formats-error")
	if options.Video || options.ExtractAudio {
		args = append(args, youtubeFormatArgs(options.Format)...)
		// 历史资产：仅传入 -f options.Format，使用引擎默认的质量与来源优先级。
	}
	if options.AutoSubtitles {
		args = append(args, "--write-auto-subs", "--sub-langs", "all,-.*")
	}
	return args
}

// youtubeFormatArgs 让解析和下载按同一分辨率顺序选流，保留自定义表达式及分辨率上限。
// 视频存在性优先，随后比较分辨率和帧率；同分辨率的编码、音轨仍由引擎选择。
func youtubeFormatArgs(format string) []string {
	return []string{"-f", format, "--format-sort", config.YouTubeFormatSort, "--format-sort-force"}
}

// youtubeHasMediaFormats 只判断音视频流是否存在，不将故事板当作可下载媒体。
// 评论和元数据专用任务允许没有音视频格式，仍使用其最新解析结果。
func youtubeHasMediaFormats(raw []byte) bool {
	var info struct {
		Formats []struct {
			Vcodec string `json:"vcodec"`
			Acodec string `json:"acodec"`
		} `json:"formats"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return false
	}
	for _, format := range info.Formats {
		if (format.Vcodec != "" && format.Vcodec != "none") || (format.Acodec != "" && format.Acodec != "none") {
			return true
		}
	}
	return false
}

// logYouTubeFormats 记录平台实际返回的可下载视频流，预览故事板不能算作视频。
// 无格式时保留原始清单，交给下载阶段刷新一次，不改为另一种画质或匿名认证。
func logYouTubeFormats(raw []byte, mediaID string, log func(string)) {
	var info struct {
		Formats []struct {
			Vcodec string `json:"vcodec"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"formats"`
		FormatID string `json:"format_id"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}
	if json.Unmarshal(raw, &info) != nil {
		return
	}
	count, width, height := 0, 0, 0
	for _, f := range info.Formats {
		if f.Vcodec == "none" || f.Vcodec == "" {
			continue
		}
		count++
		if min(f.Width, f.Height) > min(width, height) {
			width, height = f.Width, f.Height
		}
	}
	log(fmt.Sprintf("格式解析；媒体 %s；可用视频流 %d；最高尺寸 %dx%d；选中格式 %s；选中尺寸 %dx%d", mediaID, count, width, height, info.FormatID, info.Width, info.Height))
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
