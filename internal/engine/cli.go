// Package engine 通过标准命令行协议集成独立下载引擎。
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CLI 只依赖运行数据目录，不直接访问数据库或 HTTP。
type CLI struct {
	DataDir string
}

// run 使用 Context 使任务取消和服务停止能终止下载进程。
// stdout 只在 JSON 扫描时收集，下载输出流式写入日志。
// stderr 始终保留，供用户排查认证或格式错误。
func run(ctx context.Context, name string, args []string, log func(string), capture bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// 两个输出流共享进度节流，错误和阶段信息不受限制。
	output := &commandLog{log: log, now: time.Now}
	stderr := &logWriter{output: output}
	stdout := &logWriter{output: output}
	cmd.Stderr = stderr
	var out bytes.Buffer
	if capture {
		cmd.Stdout = &out
	} else {
		cmd.Stdout = stdout
	}
	e := cmd.Run()
	stderr.flush()
	stdout.flush()
	if e != nil {
		if output.lastError != "" {
			return out.Bytes(), fmt.Errorf("%s 执行失败：%s: %w", name, output.lastError, e)
		}
		return out.Bytes(), fmt.Errorf("%s 执行失败: %w", name, e)
	}
	return out.Bytes(), nil
}

// CookiePath 按引擎独立保存 Netscape Cookie 文件。
func (c CLI) CookiePath(platform string) string {
	return filepath.Join(c.DataDir, "cookies", platform+".txt")
}

// common 统一加载已配置的 Cookie 和默认配置；任务不再单独启用 Cookie。
// gallery-dl 与 yt-dlp 使用不同的忽略配置参数。
func (c CLI) common(s domain.Subscription) []string {
	args := []string{"--ignore-config"}
	if s.Platform == "youtube" {
		args = append(args, "--js-runtimes", "node", "--progress-delta", strconv.Itoa(int(config.DownloadLogInterval/time.Second)))
	}
	if s.Platform == "instagram" {
		args[0] = "--config-ignore"
		if _, e := os.Stat(filepath.Join(c.DataDir, "gallery-dl.json")); e == nil {
			args = append(args, "--config", filepath.Join(c.DataDir, "gallery-dl.json"))
		}
		// 使用平台级配置，避免 gallery-dl 的 Instagram 专用 UA 覆盖全局 --user-agent。
		userAgent := config.InstagramRequestUserAgent()
		args = append(args, "-o", "extractor.instagram.user-agent="+userAgent,
			"-o", "extractor.instagram.headers="+config.InstagramHeaders)
	}
	if _, err := os.Stat(c.CookiePath(s.Platform)); err == nil {
		args = append(args, "--cookies", c.CookiePath(s.Platform))
	}
	return args
}

// Scan 枚举当前平台可获取内容，不把引擎未返回的历史判定为完整。
// 非零退出码仍允许返回已解析的部分结果。
// 服务层同时持久化发现内容并标记扫描失败。
func (c CLI) Scan(ctx context.Context, s domain.Subscription, itemLimit int, log func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	args := c.common(s)
	name := "yt-dlp"
	if s.Platform == "youtube" {
		if domain.TaskMode(s.Platform, s.URL) == "content" {
			args = append(args, "--no-playlist")
		}
		if itemLimit > 0 {
			args = append(args, "--playlist-end", strconv.Itoa(itemLimit))
		}
		log("使用 yt-dlp 逐条枚举媒体，发现后立即加入下载队列")
		args = append(args, "--dump-json", "--flat-playlist", "--", s.URL)
	} else {
		if itemLimit > 0 {
			args = append(args, "--range", "1-"+strconv.Itoa(itemLimit))
		}
		log("使用 gallery-dl 递归流式枚举媒体，发现后立即加入下载队列")
		args = append(args, "--resolve-json", "--", s.URL)
		args = append([]string{"-u", "-c", instagramScript(instagramStream)}, args...)
		name = "python3"
	}
	return scanStream(ctx, name, args, s, log, emit)
}

// youtubeEntry 同时支持频道、播放列表和单视频元数据。
type youtubeEntry struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	URL           string         `json:"url"`
	WebURL        string         `json:"webpage_url"`
	Channel       string         `json:"channel"`
	Uploader      string         `json:"uploader"`
	PlaylistID    string         `json:"playlist_id"`
	PlaylistTitle string         `json:"playlist_title"`
	ChannelID     string         `json:"channel_id"`
	UploadDate    string         `json:"upload_date"`
	Timestamp     float64        `json:"timestamp"`
	Type          string         `json:"_type"`
	Entries       []youtubeEntry `json:"entries"`
}

// parseYouTube 保留订阅对象自身的 ID，播放列表不会与频道合并。
// 频道元数据可能包含嵌套列表，递归展开为视频内容。
// 扁平扫描没有发布日期时，下载阶段补读单视频信息。
func parseYouTube(raw []byte) (domain.ScanResult, error) {
	var root youtubeEntry
	dec := json.NewDecoder(bytes.NewReader(raw))
	if e := dec.Decode(&root); e != nil {
		return domain.ScanResult{}, e
	}
	name := root.Channel
	if name == "" {
		name = root.Uploader
	}
	if name == "" {
		name = root.Title
	}
	result := domain.ScanResult{StableID: root.ID, CurrentName: name, Items: []domain.Content{}}
	var visit func(youtubeEntry)
	visit = func(v youtubeEntry) {
		if v.Entries != nil {
			for _, child := range v.Entries {
				visit(child)
			}
			return
		}
		if v.ID == "" {
			return
		}
		u := v.WebURL
		if u == "" {
			u = v.URL
		}
		if !strings.HasPrefix(u, "http") {
			u = "https://www.youtube.com/watch?v=" + v.ID
		}
		date, _ := time.Parse("20060102", v.UploadDate)
		if date.IsZero() && v.Timestamp != 0 {
			date = time.Unix(int64(v.Timestamp), 0)
		}
		result.Items = append(result.Items, domain.Content{RemoteID: v.ID, URL: u, Title: v.Title, Published: date})
	}
	visit(root)
	return result, nil
}

// parseInstagram 读取 gallery-dl 的 [消息类型, URL, 元数据] 数组。
// 使用 json.Number 保留超出 JavaScript 精确整数范围的身份。
// 每个轮播媒体用 post_id 与 num 组合去重。
func parseInstagram(raw []byte) (domain.ScanResult, error) {
	var rows [][]json.RawMessage
	if e := json.Unmarshal(raw, &rows); e != nil {
		return domain.ScanResult{}, e
	}
	result := domain.ScanResult{Items: []domain.Content{}}
	var scanErr error
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		var kind int
		_ = json.Unmarshal(row[0], &kind)
		// DataJob 把提取失败写成 [-1, 错误对象]，进程仍可能以 0 退出。
		// 保留已发现媒体，同时把登录、请求或分页错误交回任务状态。
		if kind == -1 {
			var detail struct{ Error, Message string }
			if err := json.Unmarshal(row[1], &detail); err != nil {
				return result, err
			}
			message := fmt.Sprintf("Instagram 扫描失败（%s）：%s", detail.Error, detail.Message)
			if detail.Error == "AuthRequired" || detail.Error == "AuthenticationError" || strings.Contains(detail.Message, "/accounts/login") {
				message += "；请在系统设置中编辑并保存有效 Instagram Cookie"
			}
			scanErr = fmt.Errorf("%s", message)
			continue
		}
		if kind == 6 {
			scanErr = fmt.Errorf("Instagram 扫描未解析后续地址，未完成内容枚举")
			continue
		}
		if kind != 3 || len(row) < 3 {
			continue
		}
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(row[2]))
		dec.UseNumber()
		if e := dec.Decode(&m); e != nil {
			return result, e
		}
		id := str(m["post_id"])
		num, _ := strconv.Atoi(str(m["num"]))
		if num == 0 {
			num = 1
		}
		if id == "" {
			id = str(m["media_id"])
		}
		u := str(m["post_url"])
		if u == "" {
			u = "https://www.instagram.com/p/" + str(m["post_shortcode"]) + "/"
		}
		if result.StableID == "" {
			result.StableID = str(m["owner_id"])
			result.CurrentName = str(m["username"])
		}
		date := parseDate(str(m["date"]))
		if date.IsZero() {
			date = parseDate(str(m["post_date"]))
		}
		result.Items = append(result.Items, domain.Content{RemoteID: id + "-" + strconv.Itoa(num), URL: u, Title: str(m["description"]), Published: date, Asset: num})
	}
	return result, scanErr
}

// 引擎版本可能改变日期表示，支持标准时间和 gallery-dl 时间字符串。
// 解析不到日期返回零值，最终模板回退到下载日期。
func parseDate(v string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, e := time.Parse(layout, v); e == nil {
			return t
		}
	}
	return time.Time{}
}

// str 将可选字符串和平台数字 ID 转为统一文本。
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// Download 的工作目录属于单个任务，后处理成功后才由应用服务归位。
// 主视频、独立音频和本地提取依次运行，共用任务取消信号。
// 任一阶段失败时不清理工作文件，供手动重试。
func (c CLI) Download(ctx context.Context, s domain.Subscription, item domain.Content, o domain.YouTubeOptions, dir string, log func(string)) error {
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	args := c.common(s)
	inventory := Inventory(item, dir)
	if s.Platform == "instagram" {
		if inventory.Video() != "" || inventory.Thumbnail() {
			log("复用已下载的 Instagram 媒体")
			return nil
		}
		args = append(args, "-D", dir, "-f", "{post_id}_{num}.{extension}", "--filter", "num == "+strconv.Itoa(item.Asset), "--", item.URL)
		var output strings.Builder
		var outputMu sync.Mutex
		// stdout 与 stderr 由不同复制协程回调，共享诊断文本需要同步。
		args = append([]string{"-u", "-c", instagramScript("import gallery_dl\ngallery_dl.main()\n")}, args...)
		_, e := run(ctx, "python3", args, func(message string) {
			outputMu.Lock()
			output.WriteString(message)
			outputMu.Unlock()
			log(message)
		}, false)
		if e != nil && strings.Contains(output.String(), "redirect to login page") {
			return fmt.Errorf("Instagram 下载需要登录：请在系统设置中编辑并保存有效 Cookie：%w", e)
		}
		return e
	}
	// 单视频二次读取元数据获得发布日期，频道的扁平扫描可能没有日期。
	log("读取下载元数据；进度百分比仅表示当前媒体流，所有流下载和后处理成功后任务才完成")
	metadataArgs := append(append([]string{}, args...), "--dump-single-json", "--no-playlist", "--no-simulate", "--skip-download")
	// YouTube 仅在启用自动字幕提取时展开翻译语言。
	// 排除全部下载语言，只收集清单，避免元数据覆盖参数提前改写已有字幕。
	if o.AutoSubtitles {
		metadataArgs = append(metadataArgs, "--write-auto-subs", "--sub-langs", "all,-.*")
	}
	if o.Metadata || o.Comments {
		metadataArgs = append(metadataArgs, "--write-info-json", "--force-overwrites", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"))
	}
	if o.Comments {
		metadataArgs = append(metadataArgs, "--write-comments")
	}
	log("刷新元数据及所选评论；已有媒体文件保持不变")
	metadata, e := run(ctx, "yt-dlp", append(metadataArgs, "--", item.URL), log, true)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(dir, ".metadata.json"), metadata, 0644); e != nil {
		return e
	}
	// 媒体与元数据分别执行，强制覆盖仅用于可变的 info JSON。
	mainOptions := o
	mainOptions.Video = (o.Video || (o.ExtractAudio && inventory.Audio(o.AudioFormat, true) == "")) && inventory.Video() == ""
	mainOptions.ExtractAudio = false
	mainOptions.Metadata = false
	mainOptions.Comments = false
	mainOptions.Thumbnail = o.Thumbnail && !inventory.Thumbnail()
	if o.Subtitles || o.AutoSubtitles {
		mainOptions.SubtitleLanguages = inventory.missingSubtitles(o, metadata)
		if mainOptions.SubtitleLanguages == "" {
			mainOptions.Subtitles = false
			mainOptions.AutoSubtitles = false
		}
	}
	if !mainOptions.Video && inventory.Video() != "" && (o.Video || o.ExtractAudio) {
		log("复用已下载视频：" + inventory.Video())
	}
	if mainOptions.Video || mainOptions.Subtitles || mainOptions.AutoSubtitles || mainOptions.Thumbnail {
		base := append(append([]string{}, args...), youtubeArgs(mainOptions, dir)...)
		log("补齐缺失的视频、字幕或缩略图")
		if _, e = run(ctx, "yt-dlp", append(base, "--", item.URL), log, false); e != nil {
			return youtubeDownloadError("主视频及附加文件", e)
		}
	}

	// 额外音频是另一次远程下载；与 FFmpeg 从本地视频提取分开。
	if o.ExtraAudio && inventory.Audio(o.AudioFormat, false) == "" {
		audio := append(append([]string{}, args...), "--no-playlist", "-f", "bestaudio/best", "-x", "--audio-format", o.AudioFormat, "--audio-quality", o.AudioQuality, "-o", filepath.Join(dir, "%(title)s-%(id)s.audio.%(ext)s"), "--", item.URL)
		log("开始额外下载音频")
		if _, e = run(ctx, "yt-dlp", audio, log, false); e != nil {
			return youtubeDownloadError("额外音频", e)
		}
		log("额外音频处理完成")
	}
	if o.ExtraAudio && inventory.Audio(o.AudioFormat, false) != "" {
		log("复用已下载音频：" + inventory.Audio(o.AudioFormat, false))
	}
	if o.ExtractAudio && inventory.Audio(o.AudioFormat, true) != "" {
		log("复用本地提取音频：" + inventory.Audio(o.AudioFormat, true))
	}
	if o.ExtractAudio && inventory.Audio(o.AudioFormat, true) == "" {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		found := false
		inputs := []string{}
		if inventory.Video() != "" {
			inputs = append(inputs, inventory.Video())
		} else {
			for _, f := range entries {
				inputs = append(inputs, filepath.Join(dir, f.Name()))
			}
		}
		for _, in := range inputs {
			ext := strings.ToLower(filepath.Ext(in))
			if ext != ".mp4" && ext != ".mkv" && ext != ".webm" && ext != ".mov" {
				continue
			}
			found = true
			out := filepath.Join(dir, strings.TrimSuffix(filepath.Base(in), ext)+".extracted."+o.AudioFormat)
			a := []string{"-y", "-i", in, "-vn"}
			// m4a 采用 AAC；最高质量使用与 yt-dlp 相同的可变码率档。
			if o.AudioFormat == "m4a" {
				a = append(a, "-c:a", "aac")
				if o.AudioQuality == "0" {
					a = append(a, "-q:a", config.AACBestQuality)
				} else {
					a = append(a, "-b:a", o.AudioQuality)
				}
			}
			if o.AudioFormat == "mp3" {
				if o.AudioQuality == "0" {
					a = append(a, "-q:a", "0")
				} else {
					a = append(a, "-b:a", o.AudioQuality)
				}
			}
			a = append(a, out)
			if _, e = run(ctx, "ffmpeg", a, log, false); e != nil {
				return e
			}
		}
		if !found {
			return fmt.Errorf("本地提取音频需要已下载的视频")
		}
	}
	return nil
}

// youtubeArgs 保持字幕、评论和附加文件选项可组合。
// 仅下载字幕等附加文件时使用 skip-download。
// 本地提取需要视频文件，因此自动请求视频下载。
func youtubeArgs(o domain.YouTubeOptions, dir string) []string {
	a := []string{"--no-playlist", "--newline", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"), "--concurrent-fragments", strconv.Itoa(o.Fragments)}
	if o.Video || o.ExtractAudio {
		// 画质选择与 MP4 封装独立：合并或重封装，不重新编码视频。
		a = append(a, "-f", o.Format, "--merge-output-format", "mp4", "--remux-video", "mp4")
	} else {
		a = append(a, "--skip-download")
	}
	if o.Subtitles {
		a = append(a, "--write-subs")
	}
	if o.AutoSubtitles {
		a = append(a, "--write-auto-subs")
	}
	if o.Subtitles || o.AutoSubtitles {
		a = append(a, "--sub-langs", o.SubtitleLanguages)
	}

	if o.Comments {
		a = append(a, "--write-comments", "--write-info-json")
	}
	if o.Metadata {
		a = append(a, "--write-info-json")
	}
	if o.Thumbnail {
		a = append(a, "--write-thumbnail")
	}
	return a
}

// PublicationDate 从下载前的真实视频元数据读取发布日期。
// 内部元数据文件不作为用户下载产物归档。
func PublicationDate(dir string) time.Time {
	raw, e := os.ReadFile(filepath.Join(dir, ".metadata.json"))
	if e != nil {
		return time.Time{}
	}
	var v youtubeEntry
	if json.Unmarshal(raw, &v) != nil {
		return time.Time{}
	}
	t, _ := time.Parse("20060102", v.UploadDate)
	if t.IsZero() && v.Timestamp != 0 {
		t = time.Unix(int64(v.Timestamp), 0)
	}
	return t
}

// Versions 将 FFmpeg 单独检测，不与 Python 包版本混合。
// 单个引擎未安装只影响其状态，不隐藏其他版本。
func Versions(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, name := range []string{"gallery-dl", "yt-dlp", "ffmpeg"} {
		arg := "--version"
		if name == "ffmpeg" {
			arg = "-version"
		}
		raw, e := exec.CommandContext(ctx, name, arg).Output()
		if e != nil {
			out[name] = "未安装"
		} else {
			out[name] = strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
		}
	}
	return out
}
