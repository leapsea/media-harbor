package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// instagramRequests 共享一个平台请求时钟，SDK 只负责实际 HTTP 会话。
// 许可持有至响应头返回，媒体流传输不会阻塞后续许可或改变下载并发。
type instagramRequests struct {
	mu                 sync.Mutex
	permit             chan struct{}
	last, cooldown     time.Time
	interval, fallback time.Duration
	server             *http.Server
	address            string
	ops                map[string]*instagramOperation
}

// 每次引擎执行独立关联取消上下文和日志，不让其他任务继承 Cookie 或签名地址。
type instagramOperation struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    func(string)
	stage  string
	leases map[string]func()
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
	return &instagramRequests{permit: make(chan struct{}, 1), interval: config.InstagramRequestInterval(), fallback: config.InstagramCooldown(), ops: map[string]*instagramOperation{}}
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
	for {
		r.mu.Lock()
		due, reason := r.last.Add(r.interval), "请求间隔"
		if r.cooldown.After(due) {
			due, reason = r.cooldown, "平台 429 统一冷却"
		}
		left := time.Until(due)
		if left <= 0 {
			// 实际获得许可时推进时钟，排队本身不会提前占用下一请求时间。
			r.last = time.Now()
			r.mu.Unlock()
			return release, nil
		}
		r.mu.Unlock()
		message := fmt.Sprintf("Instagram 请求统一等待；阶段 %s；原因 %s；剩余 %s", stage, reason, left.Round(time.Millisecond))
		log(message)
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
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		}
	}
}

// rateLimited 必须先登记冷却再释放许可，避免下一请求越过刚收到的 429。
func (r *instagramRequests) rateLimited(after string) time.Duration {
	wait := r.fallback
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
	}
	return time.Until(r.cooldown)
}

// begin 启动本机控制通道；令牌只存在于本次进程环境，不公开到业务 API。
func (r *instagramRequests) begin(ctx context.Context, stage string, log func(string)) ([]string, func(), error) {
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
	op := &instagramOperation{ctx: ctx, cancel: cancel, log: log, stage: stage, leases: map[string]func(){}}
	r.ops[token] = op
	end := func() {
		// 先取消等待者再清理许可，异常退出也不会阻挡下一批请求。
		cancel()
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, release := range op.leases {
			release()
		}
		delete(r.ops, token)
		if len(r.ops) == 0 {
			// 保留平台时钟，但关闭闲置端口，不遗留后台控制服务。
			_ = r.server.Close()
			r.server = nil
		}
	}
	return []string{"MEDIA_HARBOR_CONTROL=" + r.address, "MEDIA_HARBOR_TOKEN=" + token, "MEDIA_HARBOR_STAGE=" + stage}, end, nil
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
		Kind, Lease, RetryAfter, Page, Shortcode string
		Status                                   int
		Total, Offset                            int64
	}
	if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	switch event.Kind {
	case "acquire":
		started := time.Now()
		release, err := r.wait(op.ctx, op.log, op.stage)
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
		r.mu.Unlock()
		op.log(fmt.Sprintf("Instagram HTTP 请求获准；阶段 %s；许可 %s；等待 %s", op.stage, lease, time.Since(started).Round(time.Millisecond)))
		_ = json.NewEncoder(w).Encode(map[string]string{"lease": lease})
	case "release":
		// 即使 SDK 关闭或请求异常，也归还许可；成功响应才生成传输起点。
		if event.Status == 429 {
			left := r.rateLimited(event.RetryAfter)
			op.log(fmt.Sprintf("Instagram 收到 429；所有扫描、帖子解析与头像请求统一冷却 %s", left.Round(time.Millisecond)))
		}
		r.mu.Lock()
		release := op.leases[event.Lease]
		delete(op.leases, event.Lease)
		r.mu.Unlock()
		if release != nil {
			// 头部已经返回，后续数据读取保留原下载器的流式与断点语义。
			release()
		}
		op.log(fmt.Sprintf("Instagram HTTP 响应；阶段 %s；许可 %s；状态 %d", op.stage, event.Lease, event.Status))
		if op.stage == "media" {
			op.log(fmt.Sprintf("媒体 HTTP 响应；状态 %d", event.Status))
			if event.Status == 401 || event.Status == 403 || event.Status == 404 || event.Status == 410 {
				raw, _ := json.Marshal(map[string]int{"status": event.Status})
				op.log("@instagram-source-rejected " + string(raw))
			}
		}
		if op.stage == "media" && (event.Status == 200 || event.Status == 206) {
			transfer(op.log, domain.TransferProgress{Kind: "media", Status: "running", Phase: "传输媒体", BytesTotal: event.Total, BytesDone: event.Offset})
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case "web-post":
		// 网页解析属于 Go 业务；Python 只取得 SDK 会话中的原始页面。
		post := instagramWebPost(event.Page, event.Shortcode)
		_ = json.NewEncoder(w).Encode(post)
	default:
		http.Error(w, "未知控制事件", 400)
	}
}
