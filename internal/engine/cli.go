// Package engine 通过标准命令行协议集成独立下载引擎。
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/credentials"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CLI 通过只读付费策略作下载决策，不直接访问数据库。
type CLI struct {
	cookies    *credentials.Store
	DataDir    string
	PaidPolicy domain.PaidVideoPolicy
	instagram  *instagramSourceCache
	requests   *instagramRequests
	pipelines  map[string]*platformPipeline
}

// NewCLI 将媒体快照限定在当前引擎实例，不跨账号或写入持久数据库。
func NewCLI(dataDir string, policy domain.PaidVideoPolicy) CLI {
	requests := newInstagramRequests()
	youtube := newInstagramRequests()
	youtube.platform = "YouTube"
	c := CLI{DataDir: dataDir, PaidPolicy: policy, instagram: newInstagramSourceCache(), requests: requests,
		pipelines: map[string]*platformPipeline{"instagram": newPlatformPipeline(requests, 10), "youtube": newPlatformPipeline(youtube, 2)}}
	c.cookies = credentials.New(dataDir)
	requests.cookies, youtube.cookies = c.cookies, c.cookies
	c.Configure(config.Defaults())
	return c
}

// instagramController 在服务实例中共享；字面量 CLI 仍可用于独立工具及测试。
func (c CLI) instagramController() *instagramRequests {
	if c.requests != nil {
		return c.requests
	}
	return newInstagramRequests()
}

// run 使用 Context 使任务取消和服务停止能终止下载进程。
// stdout 只在 JSON 扫描时收集，下载输出流式写入日志。
// stderr 始终保留，供用户排查认证或格式错误。
func run(ctx context.Context, name string, args []string, log func(string), capture bool) ([]byte, error) {
	return runWithEnv(ctx, name, args, log, capture, nil)
}

// runWithEnv 只为 SDK 控制通道附加本次会话变量，不修改宿主机环境。
func runWithEnv(ctx context.Context, name string, args []string, log func(string), capture bool, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
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
		if s.RequestLanguage != "" {
			args = append(args, "--extractor-args", "youtube:lang="+s.RequestLanguage)
		}
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
		// Go 统一掌握解析间隔及429期限，SDK 保留错误判断和有限重试次数。
		// 否则旧 sleep-429 会让用户缩短冷却后的设置仍等待原来的一分钟。
		// SDK 的零值表示停止429重试，最小非零值保留有限重试，实际等待由 Go 执行。
		args = append(args, "-o", "extractor.instagram.sleep-request=0", "-o", "extractor.instagram.sleep-429=0.001", "-o", "extractor.instagram.sleep-retries=0.001", "-o", "downloader.http.sleep-429=0.001")
		// Cookie回写经Go按版本合并，SDK并发进程不直接覆盖共享文件。
		if c.cookies != nil && c.cookies.Active() {
			args = append(args, "-o", "extractor.instagram.cookies-update=false")
		}
	}
	if cookie := c.activeCookie(s.Platform); cookie != "" {
		args = append(args, "--cookies", cookie)
	}
	/* 历史资产：参数构造直接检查共享Cookie文件。
	useCookie := true
	if c.cookies != nil && c.cookies.Active() {
		status, err := c.cookies.Status(s.Platform)
		useCookie = err == nil && status.Usable
	}
	if _, err := os.Stat(c.CookiePath(s.Platform)); err == nil && useCookie {
		args = append(args, "--cookies", c.CookiePath(s.Platform))
	}
	// 历史资产：文件存在即传入 --cookies，未判断认证状态。
	*/
	return args
}

// Scan 枚举当前平台可获取内容，不把引擎未返回的历史判定为完整。
// 非零退出码仍允许返回已解析的部分结果。
// 服务层同时持久化发现内容并标记扫描失败。
// SupportsPostTasks 表示正式引擎通过 Go 控制协议执行解析前核对。
func (c CLI) SupportsPostTasks() bool { return true }

func (c CLI) Scan(ctx context.Context, s domain.Subscription, itemLimit int, log func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	return c.scan(ctx, s, itemLimit, nil, log, emit)
}

// ScanIncremental 把 YouTube 本地索引交给引擎，在列表项匹配时停止后续分页。
// 临时 archive 只作为本次扫描的只读快照，不代替数据库或记录新下载。
func (c CLI) ScanIncremental(ctx context.Context, s domain.Subscription, itemLimit int, known []string, log func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	if s.Platform != "youtube" {
		return c.Scan(ctx, s, itemLimit, log, emit)
	}
	args := []string{"--lazy-playlist"}
	if len(known) > 0 {
		archive, err := os.CreateTemp("", "media-harbor-incremental-*.txt")
		if err != nil {
			return domain.ScanResult{}, err
		}
		defer os.Remove(archive.Name())
		// archive 中的身份与 yt-dlp 的 YouTube 提取器约定一致。
		for _, id := range known {
			if _, err = fmt.Fprintln(archive, "youtube "+id); err != nil {
				archive.Close()
				return domain.ScanResult{}, err
			}
		}
		if err = archive.Close(); err != nil {
			return domain.ScanResult{}, err
		}
		args = append(args, "--download-archive", archive.Name(), "--break-on-existing")
	}
	result, err := c.scan(ctx, s, itemLimit, args, log, emit)
	// dump-json 默认静默，不能依赖英文日志识别结束原因。
	// 忽略用户配置且唯一主动停止参数为 break-on-existing，101 即缓存边界。
	var exit *exec.ExitError
	if len(known) > 0 && errors.As(err, &exit) && exit.ExitCode() == 101 && ctx.Err() == nil {
		log("增量扫描命中本地缓存；YouTube 引擎停止后续历史检查与分页")
		return result, nil
	}
	return result, err
}

// scan 保留普通全量枚举入口，仅由增量追加平台停止参数。
func (c CLI) scan(ctx context.Context, s domain.Subscription, itemLimit int, extra []string, log func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	if s.Platform == "youtube" && domain.TaskMode(s.Platform, s.URL) == "content" {
		id := domain.PostIdentity(s.Platform, domain.Content{URL: s.URL})
		if id != "" {
			log("生产者：从单视频地址取得标识，数据库核对后才解析；媒体 " + id)
			return domain.ScanResult{}, emit(domain.ScanResult{Items: []domain.Content{{RemoteID: id, URL: s.URL}}})
		}
	}
	if p := c.pipeline(s.Platform); p != nil {
		release, err := p.acquireProducer(ctx, log)
		if err != nil {
			return domain.ScanResult{}, err
		}
		defer func() {
			if release != nil {
				release()
			}
		}()
		deliver := emit
		emit = func(batch domain.ScanResult) error {
			// SDK 等待 stdin 确认；释放解析执行权让恢复任务或视频准备继续。
			release()
			release = nil
			if err := deliver(batch); err != nil {
				return err
			}
			var err error
			release, err = p.acquireProducer(ctx, log)
			return err
		}
	}
	if s.Platform == "instagram" && c.instagram != nil {
		deliver := emit
		emit = func(batch domain.ScanResult) error {
			// 入队前留下地址，下载消费者无需再次解析整个帖子。
			c.instagram.put(batch)
			return deliver(batch)
		}
	}
	args := c.common(s)
	args = append(args, extra...)
	name := "yt-dlp"
	if s.Platform == "youtube" {
		if domain.TaskMode(s.Platform, s.URL) == "content" {
			args = append(args, "--no-playlist")
		}
		if itemLimit > 0 {
			args = append(args, "--playlist-end", strconv.Itoa(itemLimit))
		}
		log("使用 yt-dlp 逐条枚举媒体，发现后立即加入下载队列")
		// 会员视频即使没有可下载格式也保留索引，交由下载阶段记录失败或跳过。
		args = append(args, "--lazy-playlist", "--dump-json", "--flat-playlist", "--ignore-no-formats-error", "--", s.URL)
	} else {
		if itemLimit > 0 {
			args = append(args, "--range", "1-"+strconv.Itoa(itemLimit))
		}
		log("使用 gallery-dl 递归流式枚举媒体，发现后立即加入下载队列")
		args = append(args, "--resolve-json", "--", s.URL)
		args = append([]string{"-u", "-c", instagramScript(instagramStream)}, args...)
		name = "python3"
	}
	var env []string
	if p := c.pipeline(s.Platform); p != nil && s.Platform == "youtube" {
		var end func()
		var err error
		env, end, err = p.requests.begin(ctx, "scan", log)
		if err != nil {
			return domain.ScanResult{}, err
		}
		defer end()
		args = append([]string{"-u", "-c", youtubeScript}, args...)
		name = "python3"
	}
	if s.Platform == "instagram" {
		var end func()
		var err error
		env, end, err = c.instagramController().begin(ctx, "scan", log)
		if err != nil {
			return domain.ScanResult{}, err
		}
		defer end()
	}
	return scanStream(ctx, name, args, s, log, emit, env)
}

// youtubeEntry 同时支持频道、播放列表和单视频元数据。
type youtubeEntry struct {
	Availability  string         `json:"availability"`
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
		result.Items = append(result.Items, domain.Content{RemoteID: v.ID, URL: u, Title: v.Title, Published: date, ChannelID: v.ChannelID, Availability: v.Availability})
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
	result := domain.ScanResult{Items: []domain.Content{}, MediaSources: map[string]domain.MediaSource{}}
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
		mediaType := "photo"
		if str(m["extension"]) == "mp4" || str(m["video_url"]) != "" {
			mediaType = "video"
		}
		remoteID := id + "-" + strconv.Itoa(num)
		result.Items = append(result.Items, domain.Content{RemoteID: remoteID, URL: u, Title: str(m["description"]), Published: date, Asset: num, MediaType: mediaType, ExpectedAssets: intValue(m["count"])})
		var sourceURL string
		if err := json.Unmarshal(row[1], &sourceURL); err != nil {
			return result, err
		}
		result.MediaSources[remoteID] = domain.MediaSource{URL: sourceURL, Metadata: m}
	}
	return result, scanErr
}

func intValue(v any) int { n, _ := strconv.Atoi(str(v)); return n }

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
	// 许可按媒体条目管理，主视频及附件不能被当成休眠后的新条目。
	scope := &mediaConsumerScope{}
	ctx = context.WithValue(ctx, mediaConsumerKey{}, scope)
	defer func() {
		if scope.release != nil {
			scope.release()
		}
	}()
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	inventory := Inventory(item, dir)
	if s.Platform == "instagram" {
		return c.downloadInstagram(ctx, s, item, dir, inventory, log)
	}
	return c.downloadYouTube(ctx, s, item, o, dir, log)
}

// youtubeArgs 保持字幕、评论和附加文件选项可组合。
// 仅下载字幕等附加文件时使用 skip-download。
// 本地提取需要视频文件，因此自动请求视频下载。
func youtubeArgs(o domain.YouTubeOptions, dir string) []string {
	a := []string{"--no-playlist", "--newline", "-o", filepath.Join(dir, "%(title)s-%(id)s.%(ext)s"), "--concurrent-fragments", strconv.Itoa(o.Fragments)}
	if o.Video || o.ExtractAudio {
		// 画质选择与 MP4 封装独立：合并或重封装，不重新编码视频。
		a = append(a, youtubeFormatArgs(o.Format)...)
		a = append(a, "--merge-output-format", "mp4", "--remux-video", "mp4")
		// 历史资产：-f o.Format 与 MP4 封装一起传入，未显式指定分辨率排序。
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
	v := videoMetadata(dir)
	t, _ := time.Parse("20060102", v.UploadDate)
	if t.IsZero() && v.Timestamp != 0 {
		t = time.Unix(int64(v.Timestamp), 0)
	}
	return t
}

// PublicationTitle 使用实际下载清单里的本地化标题，避免历史沿用早期扫描语言。
func PublicationTitle(dir string) string { return videoMetadata(dir).Title }

// PublicationAccess 在文件复用及失败后同步平台提供的频道与付费标识。
func PublicationAccess(dir string) (string, string) {
	info := videoMetadata(dir)
	return info.ChannelID, info.Availability
}

func videoMetadata(dir string) youtubeEntry {
	raw, e := os.ReadFile(filepath.Join(dir, ".metadata.json"))
	if e != nil {
		return youtubeEntry{}
	}
	var v youtubeEntry
	if json.Unmarshal(raw, &v) != nil {
		return youtubeEntry{}
	}
	return v
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

// CredentialStore 由服务激活认证策略；独立工具不隐式改变平台运行规则。
func (c CLI) CredentialStore() *credentials.Store { return c.cookies }

// activeCookie 收口扫描、下载与只读验证的凭据选择；已停用Cookie不能通过旁路加载。
func (c CLI) activeCookie(platform string) string {
	if c.cookies != nil && c.cookies.Active() {
		state, err := c.cookies.Status(platform)
		if err != nil || !state.Usable {
			return ""
		}
	}
	if _, err := os.Stat(c.CookiePath(platform)); err == nil {
		return c.CookiePath(platform)
	}
	return ""
}
