package engine

import (
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/url"
	"strings"
	"time"
)

// dryProbe 定义 Go 的验证计划，SDK 只执行一次只读 HTTP 并返回内存响应。
type dryProbe struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Label   string            `json:"label"`
}
type dryFormat struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"http_headers"`
	VCodec  string            `json:"vcodec"`
	ACodec  string            `json:"acodec"`
}
type dryTrack struct {
	URL     string            `json:"url"`
	Data    string            `json:"data"`
	Headers map[string]string `json:"http_headers"`
}

// dryRunProbes 的选项、附加音频及字幕选择复用正式产物策略，不再分散在 Python。
func dryRunProbes(raw json.RawMessage, platform string, options domain.YouTubeOptions, log func(string)) ([]dryProbe, error) {
	var item struct {
		ID, URL, Thumbnail string
		Headers            map[string]string     `json:"http_headers"`
		Requested          []dryFormat           `json:"requested_formats"`
		Formats            []dryFormat           `json:"formats"`
		Subtitles          map[string][]dryTrack `json:"subtitles"`
		Automatic          map[string][]dryTrack `json:"automatic_captions"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	var probes []dryProbe
	// 同一条媒体的默认请求头必须传播给分片、音频和字幕验证。
	add := func(address string, headers map[string]string, label string) {
		if address != "" {
			if headers == nil {
				headers = item.Headers
			}
			probes = append(probes, dryProbe{address, headers, item.ID + " " + label})
		}
	}
	if platform == "instagram" {
		// Instagram 的 SDK 已给出最终媒体地址，不重新请求帖子详情。
		add(item.URL, item.Headers, "媒体")
		return probes, nil
	}
	if options.Video || options.ExtractAudio || options.ExtraAudio {
		// 格式选择由 SDK 完成，Go 只决定实际需要验证哪些已解析流。
		formats := item.Requested
		if len(formats) == 0 {
			// 单流元数据直接携带 URL，没有 requested_formats 列表。
			formats = []dryFormat{{URL: item.URL, Headers: item.Headers}}
		}
		hasAudio := false
		for i, format := range formats {
			// 已选中的独立音频不再额外验证另一条音频。
			add(format.URL, format.Headers, fmt.Sprintf("媒体流 %d", i+1))
			hasAudio = hasAudio || format.VCodec == "none"
		}
		if options.ExtraAudio && !hasAudio {
			// 与实际附属下载一致，按 SDK 格式优先级选择可用音频。
			for i := len(item.Formats) - 1; i >= 0; i-- {
				format := item.Formats[i]
				if format.VCodec == "none" && format.ACodec != "none" {
					add(format.URL, format.Headers, "附加音频")
					break
				}
			}
		}
	}
	// 原始中文优先及已有字幕选择规则来自同一 Go 产物模块。
	languages := (ArtifactInventory{}).missingSubtitles(options, raw)
	for _, language := range strings.Split(languages, ",") {
		tracks := item.Subtitles[language]
		if !options.Subtitles || len(tracks) == 0 {
			tracks = item.Automatic[language]
		}
		if len(tracks) == 0 {
			// 平台没有返回匹配字幕时不虚构一个探测请求。
			continue
		}
		track := tracks[len(tracks)-1]
		if track.Data != "" {
			// 内嵌字幕已经随元数据取得，无需发送网络请求。
			log("请求阶段：" + item.ID + " 字幕 " + language + "；已取得内存字幕")
		} else {
			add(track.URL, track.Headers, "字幕 "+language)
		}
	}
	if options.Subtitles || options.AutoSubtitles {
		log("字幕阶段：" + item.ID + "；匹配语言 " + languages)
	}
	if options.Thumbnail {
		add(item.Thumbnail, nil, "缩略图")
	}
	return probes, nil
}

// probeDryResponse 控制清单深度、首分片选择和错误语义，响应从不保存到磁盘。
func probeDryResponse(encoder *json.Encoder, decoder *json.Decoder, probe dryProbe, depth int, log func(string)) error {
	started := time.Now()
	// 同一命令通道逐条验证响应，错误时不会继续读取其他媒体。
	if err := encoder.Encode(map[string]any{"kind": "probe", "probe": probe}); err != nil {
		return err
	}
	var response struct {
		Kind, Message string
		Status        int
		Body          []byte
	}
	if err := decoder.Decode(&response); err != nil {
		return err
	}
	if response.Kind == "error" {
		// SDK 鉴权或网络错误直接保留，不能当作字幕不存在。
		return fmt.Errorf("%s", response.Message)
	}
	if response.Kind != "probe" || response.Status < 200 || response.Status >= 300 || len(response.Body) == 0 {
		return fmt.Errorf("%s 返回无效响应；HTTP %d", probe.Label, response.Status)
	}
	log(fmt.Sprintf("请求阶段：%s；HTTP %d；内存读取 %d 字节；耗时 %s", probe.Label, response.Status, len(response.Body), time.Since(started).Round(time.Millisecond)))
	if !strings.HasPrefix(strings.TrimSpace(string(response.Body)), "#EXTM3U") {
		// 普通媒体与字幕仅验证有限字节，既不落盘也不读完整文件。
		return nil
	}
	if depth >= config.DryRunManifestDepth {
		return fmt.Errorf("%s 播放清单超过探测层数，未确认分片响应", probe.Label)
	}
	// 清单 200 并不能证明实际媒体可获取，必须继续验证首个清单或分片地址。
	for _, line := range strings.Split(string(response.Body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		base, err := url.Parse(probe.URL)
		if err != nil {
			return err
		}
		child, err := url.Parse(line)
		if err != nil {
			return err
		}
		return probeDryResponse(encoder, decoder, dryProbe{base.ResolveReference(child).String(), probe.Headers, probe.Label + " / 首个分片"}, depth+1, log)
	}
	return fmt.Errorf("%s 播放清单未包含可验证的媒体地址", probe.Label)
}
