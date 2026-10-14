package httpapi

import (
	"encoding/json"
	"media-harbor/internal/domain"
	"net/http"
	"sync"
)

// dryRun 流式输出内存日志，关闭连接会取消当前请求与引擎进程。
func (api *API) dryRun(w http.ResponseWriter, r *http.Request) {
	var sub domain.Subscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	// 禁止代理缓存和响应缓冲，让进度立即到达浏览器。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	encoder := json.NewEncoder(w)
	// SDK stderr 与 Go 请求调度可同时报告日志，整条事件编码与刷新必须串行。
	var outputMu sync.Mutex
	send := func(value any) {
		outputMu.Lock()
		defer outputMu.Unlock()
		_ = encoder.Encode(value)
		_ = http.NewResponseController(w).Flush()
	}
	err := api.app.DryRun(r.Context(), sub, func(line string) { send(map[string]string{"type": "log", "message": line}) })
	// 失败仍作为同一日志流的结束事件返回，前端可以保留已产生的诊断。
	result := map[string]any{"type": "done", "ok": err == nil}
	if err != nil {
		result["error"] = err.Error()
	}
	send(result)
}
