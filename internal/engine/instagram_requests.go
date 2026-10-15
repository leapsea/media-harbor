package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/credentials"
	"media-harbor/internal/domain"
	"media-harbor/internal/logtext"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// instagramRequests 共享一个平台请求时钟，SDK 只负责实际 HTTP 会话。
// 许可持有至响应头返回，媒体流传输不会阻塞后续许可或改变下载并发。
type instagramRequests struct {
	cookies            *credentials.Store
	mu                 sync.Mutex
	permit             chan struct{}
	last, cooldown     time.Time
	interval, fallback time.Duration
	window             time.Duration
	maximum            int
	history            []time.Time
	sleep              domain.SleepSettings
	platform           string
	changed            chan struct{}
	server             *http.Server
	address            string
	ops                map[string]*instagramOperation
}

// 每次引擎执行独立关联取消上下文和日志，不让其他任务继承 Cookie 或签名地址。
type instagramOperation struct {
	revision            int64
	usesCookie          bool
	ctx                 context.Context
	cancel              context.CancelFunc
	log                 func(string)
	stage               string
	mediaID             string
	leases              map[string]func()
	timings             map[string]requestTiming
	stats               requestLogStats
	lastCommentReport   int
	lastCommentReportAt time.Time
}

// instagramRoundTripper 使 Go 的公开作者网页请求与 SDK 使用同一个平台许可。
type instagramRoundTripper struct{ requests *instagramRequests }

func (t instagramRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := t.requests.wait(req.Context(), func(string) {}, "author-page")
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultTransport.RoundTrip(req)
	if response != nil && response.StatusCode == 429 {
		t.requests.rateLimited(response.Header.Get("Retry-After"))
	}
	release()
	return response, err
}

func newInstagramRequests() *instagramRequests {
	// 环境值在引擎实例建立时固定，不在每次请求中重新解释配置。
	return &instagramRequests{permit: make(chan struct{}, 1), interval: config.InstagramRequestInterval(), fallback: config.InstagramCooldown(), platform: "Instagram", changed: make(chan struct{}), ops: map[string]*instagramOperation{}}
}

// configure 保留已发请求，缩小窗口或保存设置不能重置请求预算。
func (r *instagramRequests) configure(s domain.ProducerSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.interval = time.Duration(s.MinIntervalSeconds * float64(time.Second))
	r.fallback = time.Duration(s.CooldownSeconds * float64(time.Second))
	r.window = time.Duration(s.WindowSeconds) * time.Second
	r.maximum = s.MaxRequests
	close(r.changed)
	r.changed = make(chan struct{})
}

// wait 即使在冷却期间也可立即取消；所有引擎进程使用同一个实际发起时刻。
func (r *instagramRequests) wait(ctx context.Context, log func(string), stage string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case r.permit <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := sync.OnceFunc(func() { <-r.permit })
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	// SDK 正常结束、异常与进程取消可能同时归还同一许可，归还必须幂等。
	var lastReport time.Time
	var lastReason string
	for {
		if err := r.checkCredentials(); err != nil {
			release()
			return nil, err
		}
		r.mu.Lock()
		due, reason := r.last.Add(r.interval), "请求间隔"
		if r.cooldown.After(due) {
			due, reason = r.cooldown, "平台 429 统一冷却"
		}
		// 滑动窗口计入重试、重定向和分页等实际 HTTP 请求，不只统计帖子数。
		now := time.Now()
		for len(r.history) > 0 && now.Sub(r.history[0]) >= r.window {
			r.history = r.history[1:]
		}
		if r.maximum > 0 && len(r.history) >= r.maximum {
			boundary := r.history[len(r.history)-r.maximum].Add(r.window)
			if boundary.After(due) {
				due, reason = boundary, "生产者滑动窗口已满"
			}
		}
		// 平台解析请求在休眠窗口内等待，媒体请求走独立 waitMedia。
		if until := r.sleep.Until(now); until.After(due) {
			due, reason = until, "休眠时间段"
		}
		left := time.Until(due)
		if left <= 0 {
			// 实际获得许可时推进时钟，排队本身不会提前占用下一请求时间。
			r.last = time.Now()
			r.history = append(r.history, r.last)
			r.mu.Unlock()
			return release, nil
		}
		changed := r.changed
		used, maximum, window := len(r.history), r.maximum, r.window
		r.mu.Unlock()
		message := fmt.Sprintf("[请求等待] %s · %s · %s · 约 %s 后继续", r.platform, logtext.Stage(stage), reason, logtext.Duration(left))
		if maximum > 0 {
			message += fmt.Sprintf(" · 窗口 %d/%d 次（%s）", used, maximum, logtext.Duration(window))
		}
		// 检查频率保持原值；只控制文字频率，原因改变时立即输出。
		if left >= config.RequestWaitLogThreshold && (lastReport.IsZero() || reason != lastReason || time.Since(lastReport) >= config.RequestWaitLogInterval) {
			log(message)
			lastReport, lastReason = time.Now(), reason
		}
		// 历史文字：请求统一等待；阶段 metadata；许可编号与末尾毫秒倒计时逐行输出。
		if stage == "media" {
			// 下载保持运行状态，界面显示等待原因而不是误报零字节传输。
			transfer(log, domain.TransferProgress{Kind: "media", Status: "running", Phase: message})
		}
		// 定期重查冷却时刻，控制取消延迟并为界面提供等待原因。
		if left > config.ScanLogInterval {
			left = config.ScanLogInterval
		}
		timer := time.NewTimer(left)
		select {
		case <-timer.C:
		case <-changed:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		}
	}
}

// rateLimited 必须先登记冷却再释放许可，避免下一请求越过刚收到的 429。
func (r *instagramRequests) rateLimited(after string) time.Duration {
	r.mu.Lock()
	wait := r.fallback
	r.mu.Unlock()
	// Retry-After 是平台给出的等待要求，未提供时才使用本地默认值。
	if seconds, err := strconv.ParseFloat(after, 64); err == nil && seconds >= 0 {
		wait = time.Duration(seconds * float64(time.Second))
	} else if deadline, err := http.ParseTime(after); err == nil && time.Until(deadline) > 0 {
		wait = time.Until(deadline)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if until := time.Now().Add(wait); until.After(r.cooldown) {
		// 多处同时报告限流只能延长冷却，不能用较短响应覆盖已有期限。
		r.cooldown = until
		close(r.changed)
		r.changed = make(chan struct{})
	}
	return time.Until(r.cooldown)
}

// waitMedia 只遵守平台冷却，不占单生产者许可或解析请求预算。
// 已开始的流读取继续传输；下一次连接、重试可取消且按最新429期限等待。
func (r *instagramRequests) waitMedia(ctx context.Context, log func(string)) error {
	var lastReport time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.checkCredentials(); err != nil {
			return err
		}
		r.mu.Lock()
		left, changed := time.Until(r.cooldown), r.changed
		r.mu.Unlock()
		if left <= 0 {
			return nil
		}
		if lastReport.IsZero() || time.Since(lastReport) >= config.RequestWaitLogInterval {
			log(fmt.Sprintf("[请求等待] %s · 媒体下载 · 429 限流冷却 · 约 %s 后继续", r.platform, logtext.Duration(left)))
			lastReport = time.Now()
		}
		timer := time.NewTimer(min(left, config.ScanLogInterval))
		select {
		case <-timer.C:
		case <-changed:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// begin 启动本机控制通道；令牌只存在于本次进程环境，不公开到业务 API。
func (r *instagramRequests) begin(ctx context.Context, stage string, log func(string)) ([]string, func(), error) {
	if err := r.checkCredentials(); err != nil {
		return nil, nil, err
	}
	var cookie credentials.Status
	if r.cookies != nil && r.cookies.Active() {
		cookie, _ = r.cookies.Status(strings.ToLower(r.platform))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server == nil {
		// 只绑定回环地址，控制通道与公开业务服务使用不同的端口。
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, nil, err
		}
		r.address = "http://" + listener.Addr().String()
		r.server = &http.Server{Handler: http.HandlerFunc(r.handle)}
		server := r.server
		go func() { defer func() { _ = recover() }(); _ = server.Serve(listener) }()
	}
	var tokenBytes [32]byte
	// 每个 SDK 进程持有自己的令牌，控制事件只能关联到本次执行。
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	ctx, cancel := context.WithCancel(ctx)
	op := &instagramOperation{revision: cookie.Revision, usesCookie: cookie.Usable, ctx: ctx, cancel: cancel, log: log, stage: stage, leases: map[string]func(){}, timings: map[string]requestTiming{}}
	op.mediaID, _ = ctx.Value(requestLogMediaKey{}).(string)
	r.ops[token] = op
	end := func() {
		// 先取消等待者再清理许可，异常退出也不会阻挡下一批请求。
		cancel()
		r.mu.Lock()
		for _, release := range op.leases {
			release()
		}
		delete(r.ops, token)
		if len(r.ops) == 0 {
			// 保留平台时钟，但关闭闲置端口，不遗留后台控制服务。
			_ = r.server.Close()
			r.server = nil
		}
		stats, unfinished := op.stats, len(op.timings)
		r.mu.Unlock()
		// 日志回调不持平台锁，避免与保存设置或进度回写形成相反锁顺序。
		if stats.count > 0 {
			op.log(stats.message(r.platform, op.stage, op.mediaID, unfinished))
		}
	}
	env := []string{"MEDIA_HARBOR_CONTROL=" + r.address, "MEDIA_HARBOR_TOKEN=" + token, "MEDIA_HARBOR_STAGE=" + stage}
	if r.cookies != nil && r.cookies.Active() {
		env = append(env, "MEDIA_HARBOR_COOKIE_UPDATE_GO=1")
	}
	return env, end, nil
}

// handle 只接收 SDK 控制事件，不代理平台流量，也不记录 Cookie 或 URL 查询。
func (r *instagramRequests) handle(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	op := r.ops[req.Header.Get("X-Media-Harbor-Token")]
	r.mu.Unlock()
	if op == nil || op.ctx.Err() != nil {
		// 旧进程或其他本地程序没有有效执行令牌，不能操作平台冷却。
		http.Error(w, "引擎会话已结束", http.StatusUnauthorized)
		return
	}
	var event struct {
		Kind, Lease, RetryAfter, Page, Shortcode, AuthFailure, Host string
		URL                                                         string
		Cookies                                                     []credentials.Cookie
		SetCookies                                                  []string
		Authenticated                                               bool
		Media                                                       bool
		Status                                                      int
		Total, Offset                                               int64
	}
	if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	switch event.Kind {
	case "comment-progress":
		// SDK 只上报原始条目事件，数量累计和日志频率由本次 Go 操作维护。
		if op.stage != "comments" {
			http.Error(w, "当前阶段不是评论抓取", 400)
			return
		}
		r.mu.Lock()
		op.stats.comments += int(event.Offset)
		op.stats.commentLimit = int(event.Total)
		count, limit := op.stats.comments, op.stats.commentLimit
		report := op.lastCommentReportAt.IsZero() || count-op.lastCommentReport >= config.CommentProgressBatch ||
			(count > op.lastCommentReport && (time.Since(op.lastCommentReportAt) >= config.RequestWaitLogInterval || count == limit))
		if report {
			op.lastCommentReport, op.lastCommentReportAt = count, time.Now()
		}
		r.mu.Unlock()
		if report {
			op.log(fmt.Sprintf("[评论进度] %s · 媒体 %s · 按最新排序 · 已采集 %d/%d 条", r.platform, op.mediaID, count, limit))
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case "acquire":
		if r.cookies != nil && r.cookies.Active() && op.usesCookie {
			status, err := r.cookies.Status(strings.ToLower(r.platform))
			if err != nil || !status.Usable || status.Revision != op.revision {
				http.Error(w, "Cookie版本已停用或更新，请重新排队", 409)
				return
			}
		}
		started := time.Now()
		// CDN 字节传输不占生产者请求预算；下载并发由消费者许可控制。
		release := func() {}
		var err error
		if event.Media {
			err = r.waitMedia(op.ctx, op.log)
		} else {
			release, err = r.wait(op.ctx, op.log, op.stage)
		}
		if err != nil {
			http.Error(w, err.Error(), 499)
			return
		}
		lease := strconv.FormatInt(time.Now().UnixNano(), 10)
		r.mu.Lock()
		if op.ctx.Err() != nil {
			// 等待期间进程可能结束，登记许可前再次核对归属。
			r.mu.Unlock()
			release()
			http.Error(w, "引擎会话已结束", 499)
			return
		}
		op.leases[lease] = release
		granted := time.Now()
		op.timings[lease] = requestTiming{started: granted}
		op.stats.count++
		op.stats.waited += granted.Sub(started)
		r.mu.Unlock()
		// 历史日志逐次输出 HTTP 请求获准、媒体传输布尔值和长许可编号。
		// 正常请求只计入阶段汇总，许可仍用于内部关联与归还。
		_ = json.NewEncoder(w).Encode(map[string]string{"lease": lease})
	case "release":
		// 即使 SDK 关闭或请求异常，也归还许可；成功响应才生成传输起点。
		if event.Status == 429 {
			left := r.rateLimited(event.RetryAfter)
			op.log(fmt.Sprintf("%s 收到 429；所有扫描、帖子解析与头像请求统一冷却 %s", r.platform, left.Round(time.Millisecond)))
		}
		r.mu.Lock()
		release := op.leases[event.Lease]
		delete(op.leases, event.Lease)
		if timing, exists := op.timings[event.Lease]; exists {
			op.stats.response += time.Since(timing.started)
			if event.Status >= 200 && event.Status < 300 {
				op.stats.success++
			} else if event.Status >= 300 && event.Status < 400 {
				op.stats.redirected++
			} else {
				op.stats.failed++
			}
			delete(op.timings, event.Lease)
		}
		r.mu.Unlock()
		if release != nil {
			// 头部已经返回，后续数据读取保留原下载器的流式与断点语义。
			release()
		}
		// 只接收原始认证信号，固定业务原因由Go生成；媒体CDN拒绝不代表Cookie失效。
		if r.cookies != nil && r.cookies.Active() && op.usesCookie && !event.Media && trustedCookieHost(strings.ToLower(r.platform), event.Host) {
			if event.AuthFailure != "" {
				reason := credentials.FailureReason(strings.ToLower(r.platform), event.AuthFailure)
				if reason != "" {
					if _, err := r.cookies.Disable(strings.ToLower(r.platform), op.revision, reason); err != nil {
						op.log("认证状态保存失败：" + err.Error())
					}
					op.log(reason)
				}
			} else {
				updates := event.Cookies
				response := &http.Response{Header: http.Header{"Set-Cookie": event.SetCookies}}
				for _, cookie := range response.Cookies() {
					domain := cookie.Domain
					if domain == "" {
						domain = event.Host
					}
					expires := int64(0)
					if !cookie.Expires.IsZero() {
						expires = cookie.Expires.Unix()
					}
					if cookie.MaxAge > 0 {
						expires = time.Now().Unix() + int64(cookie.MaxAge)
					}
					if cookie.MaxAge < 0 {
						expires = 1
					}
					updates = append(updates, credentials.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: domain, Path: cookie.Path, Expires: expires, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly})
				}
				if op.stage != "dryrun" {
					_ = r.cookies.Apply(strings.ToLower(r.platform), op.revision, updates)
				}
				if event.Authenticated && event.Status == 200 {
					_ = r.cookies.Confirm(strings.ToLower(r.platform), op.revision)
				}
			}
		}
		// 异常逐条保留；成功响应由本次阶段的请求汇总代替重复的200行。
		if event.Status == 0 {
			op.log(fmt.Sprintf("[请求异常] %s · %s · 连接失败，未收到 HTTP 状态", r.platform, logtext.Stage(op.stage)))
		} else if event.Status >= 400 {
			op.log(fmt.Sprintf("[请求异常] %s · %s · HTTP %d", r.platform, logtext.Stage(op.stage), event.Status))
		}
		// 历史日志：HTTP 响应；阶段 metadata；许可 179...；状态 200。
		if op.stage == "media" && r.platform == "Instagram" {
			op.log(fmt.Sprintf("媒体 HTTP 响应；状态 %d", event.Status))
			if event.Status == 401 || event.Status == 403 || event.Status == 404 || event.Status == 410 {
				raw, _ := json.Marshal(map[string]int{"status": event.Status})
				op.log("@instagram-source-rejected " + string(raw))
			}
		}
		if op.stage == "media" && r.platform == "Instagram" && (event.Status == 200 || event.Status == 206) {
			transfer(op.log, domain.TransferProgress{Kind: "media", Status: "running", Phase: "传输媒体", BytesTotal: event.Total, BytesDone: event.Offset})
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case "sdk-error":
		if r.cookies != nil && r.cookies.Active() && op.usesCookie {
			if reason := credentials.FailureReason(strings.ToLower(r.platform), event.AuthFailure); reason != "" {
				if changed, err := r.cookies.Disable(strings.ToLower(r.platform), op.revision, reason); err != nil {
					op.log("认证状态保存失败：" + err.Error())
				} else if changed {
					op.log("Cookie已停用；" + reason)
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case "post-check":
		action, err := domain.GatePost(op.ctx, domain.PostCandidate{RemoteID: event.Shortcode, URL: event.URL})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"action": action})
	case "web-post":
		// 网页解析属于 Go 业务；Python 只取得 SDK 会话中的原始页面。
		post := instagramWebPost(event.Page, event.Shortcode)
		_ = json.NewEncoder(w).Encode(post)
	default:
		http.Error(w, "未知控制事件", 400)
	}
}

// checkCredentials 复用平台策略，YouTube无Cookie时仍允许匿名使用。
func (r *instagramRequests) checkCredentials() error {
	if r.cookies != nil && r.cookies.Active() {
		return r.cookies.Check(strings.ToLower(r.platform))
	}
	return nil
}

// trustedCookieHost 排除媒体CDN和其他网站的401/403，防止误停用登录。
func trustedCookieHost(platform, host string) bool {
	return host == platform+".com" || strings.HasSuffix(host, "."+platform+".com")
}
