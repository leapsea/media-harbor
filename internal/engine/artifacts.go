package engine

import (
	"encoding/json"
	"media-harbor/internal/domain"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ArtifactInventory 对照当前内容索引和本任务工作目录，复用真实存在的已完成文件。
// 不扫描其他任务目录或恢复旧数据库数据；部分下载与引擎临时文件不算完成产物。
type ArtifactInventory struct{ Paths []string }

// CompletedArtifact 排除内部元数据、分片及下载中间文件。
func CompletedArtifact(path string) bool {
	name := filepath.Base(path)
	if strings.HasPrefix(name, ".") || strings.Contains(name, ".part") || strings.HasSuffix(name, ".ytdl") || strings.HasSuffix(name, ".tmp") || strings.Contains(name, ".temp.") {
		return false
	}
	if fragmentFile.MatchString(name) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

var fragmentFile = regexp.MustCompile(`\.f\d+\.(mp4|webm|m4a)$`)

// Inventory 合并索引与本次工作目录，重试可复用尚未归位的完整产物。
func Inventory(item domain.Content, dir string) ArtifactInventory {
	result := ArtifactInventory{}
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] && CompletedArtifact(path) {
			seen[path] = true
			result.Paths = append(result.Paths, path)
		}
	}
	for _, path := range item.Paths {
		add(path)
	}
	if dir != "" {
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				add(path)
			}
			return nil
		})
	}
	return result
}
func (v ArtifactInventory) Video() string {
	for _, path := range v.Paths {
		// 原始音轨可以使用 WebM 容器，不能把带音频标识的文件当作主视频。
		if strings.Contains(filepath.Base(path), ".audio.") || strings.Contains(filepath.Base(path), ".extracted.") {
			continue
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4", ".mkv", ".webm", ".mov":
			return path
		}
	}
	return ""
}
func (v ArtifactInventory) Audio(format string, extracted bool) string {
	for _, path := range v.Paths {
		isExtracted := strings.Contains(filepath.Base(path), ".extracted.")
		if format == "best" && isExtracted == extracted {
			// 标识覆盖原始容器；纯音频扩展名兼容已有索引，旧文件不会被重新编码。
			if isExtracted || strings.Contains(filepath.Base(path), ".audio.") {
				return path
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".m4a", ".opus", ".ogg", ".mp3", ".aac", ".flac", ".wav", ".mka", ".ac3", ".eac3":
				return path
			}
		}
		if strings.EqualFold(filepath.Ext(path), "."+format) && isExtracted == extracted {
			return path
		}
	}
	return ""
}
func (v ArtifactInventory) Thumbnail() bool {
	for _, path := range v.Paths {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".jpg", ".jpeg", ".webp", ".png":
			return true
		}
	}
	return false
}
func (v ArtifactInventory) subtitle(language string) bool {
	for _, path := range v.Paths {
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".vtt", ".srt", ".ass", ".ttml", ".srv1", ".srv2", ".srv3", ".json3":
			stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if strings.HasSuffix(stem, "."+language) {
				return true
			}
		}
	}
	return false
}

// missingSubtitles 以平台本次提供的语言计算缺项，已有相同语言不重复获取。
// 不存在可用字幕时不将用户选择的语言冒充已下载文件。
func (v ArtifactInventory) missingSubtitles(options domain.YouTubeOptions, metadata []byte) string {
	var available struct {
		Subtitles map[string]json.RawMessage `json:"subtitles"`
		Auto      map[string]json.RawMessage `json:"automatic_captions"`
	}
	_ = json.Unmarshal(metadata, &available)
	availableLanguages := map[string]bool{}
	if options.Subtitles {
		for lang := range available.Subtitles {
			availableLanguages[lang] = true
		}
	}
	if options.AutoSubtitles {
		for lang := range available.Auto {
			availableLanguages[lang] = true
		}
	}
	languages := map[string]bool{}
	for _, pattern := range strings.Split(options.SubtitleLanguages, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		exclude := strings.HasPrefix(pattern, "-")
		if exclude {
			pattern = strings.TrimPrefix(pattern, "-")
		}
		if pattern == "all" {
			pattern = ".*"
		}
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		// 无效表达式交给原引擎报告，避免静默忽略用户的字幕配置。
		if err != nil {
			return options.SubtitleLanguages
		}
		for lang := range availableLanguages {
			if !re.MatchString(lang) {
				continue
			}
			if exclude {
				delete(languages, lang)
			} else {
				languages[lang] = true
			}
		}
	}
	// 在去重前选择原始中文：已下载原始字幕时也不再请求中文翻译版本。
	preferOriginalChinese(languages, available.Subtitles, available.Auto, options.Subtitles)
	for lang := range languages {
		if v.subtitle(lang) {
			delete(languages, lang)
		}
	}
	// 语言配置的顺序决定优先级，同一语言组只返回一份字幕。
	// 已有符合条件的字幕即满足需求，不额外补另一种语言。
	for lang := range availableLanguages {
		if v.subtitle(lang) && subtitleMatches(options.SubtitleLanguages, lang) {
			return ""
		}
	}
	for _, pattern := range strings.Split(options.SubtitleLanguages, ",") {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" || strings.HasPrefix(pattern, "-") {
			continue
		}
		if pattern == "all" {
			pattern = ".*"
		}
		re, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return options.SubtitleLanguages
		}
		choices := []string{}
		for lang := range languages {
			if re.MatchString(lang) {
				choices = append(choices, lang)
			}
		}
		sort.Slice(choices, func(i, j int) bool {
			a, b := choices[i], choices[j]
			rank := func(lang string) int {
				if options.Subtitles {
					if _, ok := available.Subtitles[lang]; ok {
						return 0
					}
				}
				if !translatedCaption(lang, available.Auto[lang]) {
					return 1
				}
				return 2
			}
			if rank(a) != rank(b) {
				return rank(a) < rank(b)
			}
			return a < b
		})
		if len(choices) > 0 {
			return choices[0]
		}
	}
	return ""
}

// subtitleMatches 按配置表达式与排除条件判断已有字幕是否满足单份需求。
func subtitleMatches(patterns, language string) bool {
	selected := false
	for _, pattern := range strings.Split(patterns, ",") {
		pattern = strings.TrimSpace(pattern)
		exclude := strings.HasPrefix(pattern, "-")
		pattern = strings.TrimPrefix(pattern, "-")
		if pattern == "all" {
			pattern = ".*"
		}
		if re, err := regexp.Compile("^(?:" + pattern + ")$"); err == nil && re.MatchString(language) {
			selected = !exclude
		}
	}
	return selected
}

// preferOriginalChinese 保留配置匹配的原始中文轨道；不存在时沿用中文翻译候选。
// 人工字幕为原始轨道，自动字幕依据翻译请求参数判断，不依赖特定视频或语言后缀。
func preferOriginalChinese(selected map[string]bool, manual, automatic map[string]json.RawMessage, manualEnabled bool) {
	original := map[string]bool{}
	for lang := range selected {
		if !isChineseSubtitle(lang) {
			continue
		}
		if _, ok := manual[lang]; ok && manualEnabled {
			original[lang] = true
		} else if !translatedCaption(lang, automatic[lang]) {
			original[lang] = true
		}
	}
	if len(original) == 0 {
		return
	}
	for lang := range selected {
		if isChineseSubtitle(lang) && !original[lang] {
			delete(selected, lang)
		}
	}
}

// 中文语言码包含 zh 及其地区、字形和翻译来源变体。
func isChineseSubtitle(language string) bool {
	language = strings.ToLower(language)
	return language == "zh" || strings.HasPrefix(language, "zh-")
}

// translatedCaption 使用 yt-dlp 提供的字幕 URL 中 tlang 参数识别机器翻译。
// -orig 为引擎明确标注的原始轨道；缺少翻译信息的普通轨道按原始字幕处理。
func translatedCaption(language string, raw json.RawMessage) bool {
	if strings.HasSuffix(language, "-orig") {
		return false
	}
	var formats []struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &formats)
	for _, format := range formats {
		address, err := url.Parse(format.URL)
		if err == nil && address.Query().Get("tlang") != "" {
			return true
		}
	}
	return false
}

// NeedsSupplement 让手动补全按产物需求执行，元数据及评论每次显式补全都会刷新。
// 普通全量与增量扫描仍只入队新内容，不触发已有内容的元数据更新。
func NeedsSupplement(platform string, item domain.Content, options domain.YouTubeOptions) bool {
	inventory := Inventory(item, "")
	if platform == "instagram" {
		return inventory.Video() == "" && !inventory.Thumbnail()
	}
	if options.Metadata || options.Comments {
		return true
	}
	if (options.Video || (options.ExtractAudio && inventory.Audio(options.AudioFormat, true) == "")) && inventory.Video() == "" {
		return true
	}
	if options.ExtraAudio && inventory.Audio("best", false) == "" {
		return true
	}
	if options.ExtractAudio && inventory.Audio(options.AudioFormat, true) == "" {
		return true
	}
	if options.Thumbnail && !inventory.Thumbnail() {
		return true
	}
	// 当前已保存的 info JSON 提供字幕语言清单；没有清单时补全执行读取最新平台元数据。
	if options.Subtitles || options.AutoSubtitles {
		for _, path := range inventory.Paths {
			if strings.HasSuffix(path, ".info.json") {
				raw, err := os.ReadFile(path)
				if err == nil {
					return inventory.missingSubtitles(options, raw) != ""
				}
			}
		}
		return true
	}
	return false
}
