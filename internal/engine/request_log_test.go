package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 正式控制事件验证成功合并、失败保留与媒体身份；不访问真实平台或依赖SDK日志措辞。
func TestRequestLogSummaryPreservesFailures(t *testing.T) {
	r := newInstagramRequests()
	r.interval = 0
	r.platform = "YouTube"
	logs := []string{}
	env, end, err := r.begin(withRequestLogMedia(context.Background(), "video-id"), "metadata", func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if end != nil {
			end()
		}
	}()
	token := strings.TrimPrefix(env[1], "MEDIA_HARBOR_TOKEN=")
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("X-Media-Harbor-Token", token)
		w := httptest.NewRecorder()
		r.handle(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	for _, status := range []int{200, 302, 503} {
		var acquired struct {
			Lease string `json:"lease"`
		}
		if err := json.Unmarshal(call(`{"Kind":"acquire"}`).Body.Bytes(), &acquired); err != nil {
			t.Fatal(err)
		}
		call(fmt.Sprintf(`{"Kind":"release","Lease":%q,"Status":%d}`, acquired.Lease, status))
	}
	end()
	end = nil
	text := strings.Join(logs, "\n")
	if strings.Contains(text, "许可") || strings.Contains(text, "HTTP 请求获准") || strings.Contains(text, "状态 200") {
		t.Fatal("成功请求仍逐条刷屏", text)
	}
	if !strings.Contains(text, "HTTP 503") || !strings.Contains(text, "请求 3 次") || !strings.Contains(text, "成功 1 / 异常 1") || !strings.Contains(text, "重定向 1 次") || !strings.Contains(text, "媒体 video-id") {
		t.Fatal(text)
	}
}

// 等待超过一次检查周期仍只报告一次，不输出结束前的毫秒倒计时；许可仍按原时刻获得。
func TestRequestWaitLogDoesNotRepeatCountdown(t *testing.T) {
	r := newInstagramRequests()
	r.interval = 6 * time.Second
	r.last = time.Now()
	logs := []string{}
	release, err := r.wait(context.Background(), func(s string) { logs = append(logs, s) }, "metadata")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(logs) != 1 || !strings.Contains(logs[0], "视频信息解析") || !strings.Contains(logs[0], "请求间隔") {
		t.Fatal(logs)
	}
}

// 评论事件独立计数，首次、每十条和达到上限时才展示，避免把分页等待误标为元数据。
func TestCommentProgressReportsCountAndStage(t *testing.T) {
	r := newInstagramRequests()
	r.platform = "YouTube"
	logs := []string{}
	env, end, err := r.begin(withRequestLogMedia(context.Background(), "comment-video"), "comments", func(s string) { logs = append(logs, s) })
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(env[1], "MEDIA_HARBOR_TOKEN=")
	for i := 0; i <= 35; i++ {
		delta := 1
		if i == 0 {
			delta = 0
		}
		req := httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"Kind":"comment-progress","Offset":%d,"Total":35}`, delta)))
		req.Header.Set("X-Media-Harbor-Token", token)
		w := httptest.NewRecorder()
		r.handle(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	end()
	if len(logs) != 5 || !strings.Contains(logs[0], "已采集 0/35 条") || !strings.Contains(logs[4], "媒体 comment-video · 按最新排序 · 已采集 35/35 条") {
		t.Fatal(logs)
	}
	summary := (requestLogStats{comments: 35, commentLimit: 35}).message("YouTube", "comments", "comment-video", 0)
	if !strings.Contains(summary, "评论抓取 · 媒体 comment-video") || !strings.Contains(summary, "已采集评论 35/35 条") || strings.Contains(summary, "视频信息解析") {
		t.Fatal(summary)
	}
}
