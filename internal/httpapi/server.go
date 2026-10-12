// Package httpapi 将应用服务暴露为同源 JSON API 和静态 Dashboard。
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// API 不引入鉴权、限流或其他防御中间件，适用于 V1 单用户部署。
type API struct{ app *service.App }

// Handler 路由显式调用应用服务；不会直接构造下载命令。
// GET 提供领域快照，写操作进入应用服务维护业务状态。
// 日志、Cookie 和配置使用独立接口，避免混入订阅结构。
func Handler(app *service.App) http.Handler {
	api := &API{app}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/subscriptions/dryrun", api.dryRun)
	mux.HandleFunc("GET /api/dashboard", api.dashboard)
	mux.HandleFunc("GET /api/subscriptions", func(w http.ResponseWriter, r *http.Request) { v, e := app.SubscriptionViews(); reply(w, v, e) })
	// 头像由同源接口读取本地文件，避免前端依赖带签名的 Instagram CDN 地址。
	mux.HandleFunc("GET /api/subscriptions/{id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		path, err := app.AvatarFile(id(r))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, path)
	})
	mux.HandleFunc("POST /api/subscriptions/resolve-name", func(w http.ResponseWriter, r *http.Request) {
		var input domain.Subscription
		err := json.NewDecoder(r.Body).Decode(&input)
		var name string
		if err == nil {
			name, err = app.ResolveName(r.Context(), input)
		}
		reply(w, map[string]string{"name": name}, err)
	})
	mux.HandleFunc("POST /api/subscriptions", api.saveSubscription)
	mux.HandleFunc("PUT /api/subscriptions/{id}", api.saveSubscription)
	mux.HandleFunc("DELETE /api/subscriptions/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, app.DeleteSubscription(id(r)))
	})
	mux.HandleFunc("POST /api/subscriptions/{id}/scan", func(w http.ResponseWriter, r *http.Request) {
		kind := r.URL.Query().Get("kind")
		if kind == "" {
			kind = "scan"
		}
		v, e := app.QueueScan(id(r), kind)
		reply(w, v, e)
	})
	mux.HandleFunc("GET /api/task-groups", func(w http.ResponseWriter, r *http.Request) { v, e := app.TaskGroups(); reply(w, v, e) })
	mux.HandleFunc("GET /api/subscriptions/{id}/details", func(w http.ResponseWriter, r *http.Request) { v, e := app.TaskDetails(id(r)); reply(w, v, e) })
	mux.HandleFunc("GET /api/subscriptions/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		v, e := app.GroupLog(id(r))
		reply(w, map[string]string{"log": v}, e)
	})
	mux.HandleFunc("POST /api/subscriptions/{id}/tasks/cancel", func(w http.ResponseWriter, r *http.Request) {
		n, e := app.GroupAction(id(r), "cancel")
		reply(w, map[string]int{"count": n}, e)
	})
	mux.HandleFunc("POST /api/subscriptions/{id}/tasks/retry", func(w http.ResponseWriter, r *http.Request) {
		n, e := app.GroupAction(id(r), "retry")
		reply(w, map[string]int{"count": n}, e)
	})
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) { v, e := app.Repo.Tasks(); reply(w, v, e) })
	mux.HandleFunc("POST /api/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, app.CancelTask(id(r)))
	})
	mux.HandleFunc("POST /api/tasks/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, app.RetryTask(id(r)))
	})
	mux.HandleFunc("GET /api/tasks/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		raw, e := app.TaskLog(id(r))
		reply(w, map[string]string{"log": raw}, e)
	})
	mux.HandleFunc("DELETE /api/contents/history", func(w http.ResponseWriter, r *http.Request) {
		count, err := app.ClearDownloadHistory()
		reply(w, map[string]int{"cleared": count}, err)
	})
	mux.HandleFunc("GET /api/contents", func(w http.ResponseWriter, r *http.Request) { v, e := app.ContentHistory(); reply(w, v, e) })
	mux.HandleFunc("POST /api/contents/{id}/archive", func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]bool{"ok": true}, app.RetryArchive(id(r)))
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) { v, e := app.Settings(); reply(w, v, e) })
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var v domain.Settings
		e := json.NewDecoder(r.Body).Decode(&v)
		if e == nil {
			e = app.SaveSettings(v)
		}
		reply(w, v, e)
	})
	mux.HandleFunc("POST /api/directories/check", func(w http.ResponseWriter, r *http.Request) { reply(w, app.Redetect(), nil) })
	mux.HandleFunc("GET /api/storage", func(w http.ResponseWriter, r *http.Request) { v, e := app.StorageStatus(); reply(w, v, e) })
	mux.HandleFunc("DELETE /api/storage/logs", func(w http.ResponseWriter, r *http.Request) {
		n, e := app.ClearLogs()
		reply(w, map[string]int{"cleared": n}, e)
	})
	mux.HandleFunc("DELETE /api/storage/work", func(w http.ResponseWriter, r *http.Request) {
		n, e := app.CleanWork()
		reply(w, map[string]int{"cleaned": n}, e)
	})
	mux.HandleFunc("GET /api/engines", api.engines)
	mux.HandleFunc("GET /api/cookies/{platform}", api.cookieStatus)
	mux.HandleFunc("GET /api/cookies/{platform}/content", api.cookieContent)
	mux.HandleFunc("PUT /api/cookies/{platform}", api.saveCookie)
	mux.HandleFunc("DELETE /api/cookies/{platform}", api.deleteCookie)
	mux.HandleFunc("GET /api/gallery-config", api.getGalleryConfig)
	mux.HandleFunc("PUT /api/gallery-config", api.saveGalleryConfig)
	// 构建后的 Vue 文件由 Go 提供，Docker 中无需运行 Node 服务。
	mux.Handle("/", http.FileServer(http.Dir(app.Config.WebDir)))
	return mux
}

// reply 统一返回 error 字段，使前端和任务错误交互保持一致。
// 所有操作错误使用同一 JSON 结构，界面统一展示。
func reply(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		status := http.StatusBadRequest
		if storage.IsMissing(e) {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// id 从服务端路由变量读取内部记录主键。
func id(r *http.Request) int64 { v, _ := strconv.ParseInt(r.PathValue("id"), 10, 64); return v }

// saveSubscription 把 CRUD 和调度配置交给应用层维护。
// 新建忽略客户端主键，编辑使用 URL 指定的记录。
func (api *API) saveSubscription(w http.ResponseWriter, r *http.Request) {
	var v domain.Subscription
	e := json.NewDecoder(r.Body).Decode(&v)
	if e == nil {
		if r.Method == http.MethodPut {
			v.ID = id(r)
		} else {
			v.ID = 0
		}
		e = api.app.SaveSubscription(&v)
	}
	reply(w, v, e)
}

// dashboard 聚合轻量数据；引擎版本单独请求以避免每次刷新运行子进程。
// 最近内容数量由界面设置决定，完整历史由 contents 接口提供。
func (api *API) dashboard(w http.ResponseWriter, r *http.Request) {
	subs, e := api.app.Repo.Subscriptions()
	if e != nil {
		reply(w, nil, e)
		return
	}
	tasks, e := api.app.Repo.Tasks()
	if e != nil {
		reply(w, nil, e)
		return
	}
	items, e := api.app.Repo.DownloadHistory()
	counts := map[string]int{"queued": 0, "running": 0, "completed": 0, "failed": 0}
	for _, t := range tasks {
		counts[t.Status]++
	}
	// 总览数量来自统一界面设置，不影响下载历史或任务队列。
	settings, err := api.app.Repo.Settings()
	if err != nil {
		reply(w, nil, err)
		return
	}
	if len(items) > settings.UI.RecentDownloads {
		items = items[:settings.UI.RecentDownloads]
	}
	reply(w, map[string]any{"version": config.ReleaseVersion(), "subscriptions": len(subs), "counts": counts, "recent": items, "directories": api.app.Directories()}, e)
}

// engines 仅提供独立运行依赖的当前版本；V1 更新通过重建镜像完成。
// 版本检测是按需操作，不在每四秒刷新中执行。
func (api *API) engines(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	v := engine.Versions(ctx)
	if recorder, ok := api.app.Repo.(interface{ RecordVersion(string, string) error }); ok {
		for k, value := range v {
			if e := recorder.RecordVersion(k, value); e != nil {
				slog.Error("记录引擎版本失败", "error", e)
			}
		}
	}
	reply(w, map[string]any{"versions": v, "update": "通过固定版本重新构建镜像"}, nil)
}

// Cookie 路径按平台独立保存；编辑文本不在日志中记录原文。
// 平台名对应固定 Cookie 文件，扫描和下载在已配置时自动使用。
func (api *API) cookiePath(r *http.Request) (string, error) {
	p := r.PathValue("platform")
	if p != "youtube" && p != "instagram" {
		return "", fmt.Errorf("平台无效")
	}
	return filepath.Join(api.app.Config.DataDir, "cookies", p+".txt"), nil
}

// cookieStatus 不读取 Cookie 内容，仅返回存在状态。
func (api *API) cookieStatus(w http.ResponseWriter, r *http.Request) {
	p, e := api.cookiePath(r)
	present := false
	if e == nil {
		_, statErr := os.Stat(p)
		present = statErr == nil
	}
	reply(w, map[string]bool{"configured": present}, e)
}

// cookieContent 仅在用户编辑时读取文本，未配置时返回空编辑内容。
func (api *API) cookieContent(w http.ResponseWriter, r *http.Request) {
	p, e := api.cookiePath(r)
	var raw []byte
	if e == nil {
		raw, e = os.ReadFile(p)
		if os.IsNotExist(e) {
			e = nil
		}
	}
	reply(w, map[string]string{"content": string(raw)}, e)
}

// saveCookie 将浏览器请求头转为引擎标准格式，Netscape 文本保持原样，响应仅包含配置状态。
func (api *API) saveCookie(w http.ResponseWriter, r *http.Request) {
	p, e := api.cookiePath(r)
	if e == nil {
		var input struct {
			Content string `json:"content"`
		}
		e = json.NewDecoder(r.Body).Decode(&input)
		if e == nil {
			var normalized string
			normalized, e = engine.NormalizeCookies(r.PathValue("platform"), input.Content)
			if e == nil {
				e = os.WriteFile(p, []byte(normalized), 0600)
			}
		}
	}
	reply(w, map[string]bool{"configured": e == nil}, e)
}

// deleteCookie 删除指定引擎的配置，未配置时仍可成功返回。
func (api *API) deleteCookie(w http.ResponseWriter, r *http.Request) {
	p, e := api.cookiePath(r)
	if e == nil {
		e = os.Remove(p)
		if os.IsNotExist(e) {
			e = nil
		}
	}
	reply(w, map[string]bool{"configured": false}, e)
}

// 配置编辑只验证 JSON 语法，具体 gallery-dl 配置由引擎解释。
// 未保存配置时返回空对象，便于首次在线编辑。
func (api *API) getGalleryConfig(w http.ResponseWriter, r *http.Request) {
	raw, e := os.ReadFile(filepath.Join(api.app.Config.DataDir, "gallery-dl.json"))
	if os.IsNotExist(e) {
		raw = []byte("{}")
		e = nil
	}
	reply(w, map[string]string{"config": string(raw)}, e)
}

// 引擎专属字段由 gallery-dl 解释，应用只保存 JSON。
func (api *API) saveGalleryConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Config string `json:"config"`
	}
	e := json.NewDecoder(r.Body).Decode(&body)
	if e == nil && !json.Valid([]byte(body.Config)) {
		e = fmt.Errorf("配置必须为有效 JSON")
	}
	if e == nil {
		e = os.WriteFile(filepath.Join(api.app.Config.DataDir, "gallery-dl.json"), []byte(body.Config), 0600)
	}
	reply(w, map[string]bool{"ok": e == nil}, e)
}
