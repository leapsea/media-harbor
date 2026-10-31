package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 可在线更新的 Python 引擎；FFmpeg 由系统包提供，仍随镜像更新。
var updatableEngines = map[string]string{
	"yt-dlp":     "yt-dlp[default,curl-cffi]",
	"gallery-dl": "gallery-dl",
}

// Updater 把更新后的引擎安装到数据目录并通过 PYTHONPATH 优先加载。
// 镜像目录归 root 所有，运行用户不可写；数据目录随容器重建保留。
// 每个版本使用独立目录，已启动的进程继续使用旧目录，不会混入新旧模块。
type Updater struct {
	dir      string
	mu       sync.Mutex
	builtin  map[string]string
	manifest map[string]string
}

// EngineStatus 同时给出当前与最新版本，界面据此决定是否可以更新。
type EngineStatus struct {
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Update  bool   `json:"update"`
	Error   string `json:"error,omitempty"`
}

// NewUpdater 先记录镜像内置版本，再丢弃不高于内置版本的旧覆盖，最后激活剩余覆盖。
func NewUpdater(dataDir string) *Updater {
	u := &Updater{dir: filepath.Join(dataDir, "engines"), builtin: map[string]string{}, manifest: map[string]string{}}
	for name := range updatableEngines {
		u.builtin[name] = engineVersion(context.Background(), name, "")
	}
	if raw, err := os.ReadFile(filepath.Join(u.dir, "manifest.json")); err == nil {
		_ = json.Unmarshal(raw, &u.manifest)
	}
	for name, folder := range u.manifest {
		version := strings.TrimPrefix(folder, name+"-")
		if _, err := os.Stat(filepath.Join(u.dir, folder)); err != nil || compareVersions(version, u.builtin[name]) <= 0 {
			delete(u.manifest, name)
			_ = os.RemoveAll(filepath.Join(u.dir, folder))
		}
	}
	u.save()
	u.activate()
	return u
}

func (u *Updater) save() {
	if len(u.manifest) == 0 {
		_ = os.Remove(filepath.Join(u.dir, "manifest.json"))
		return
	}
	raw, _ := json.Marshal(u.manifest)
	_ = os.MkdirAll(u.dir, 0755)
	_ = os.WriteFile(filepath.Join(u.dir, "manifest.json"), raw, 0644)
}

// activate 使用目录真实路径，之后启动的 yt-dlp、gallery-dl 与 python3 都继承该变量。
func (u *Updater) activate() {
	var paths []string
	for _, folder := range u.manifest {
		paths = append(paths, filepath.Join(u.dir, folder))
	}
	if len(paths) == 0 {
		os.Unsetenv("PYTHONPATH")
		return
	}
	os.Setenv("PYTHONPATH", strings.Join(paths, string(os.PathListSeparator)))
}

// Status 并发查询 PyPI；网络失败只影响对应引擎。
func (u *Updater) Status(ctx context.Context) map[string]EngineStatus {
	out := map[string]EngineStatus{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name := range updatableEngines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := EngineStatus{Current: engineVersion(ctx, name, "keep")}
			latest, err := latestVersion(ctx, name)
			if err != nil {
				s.Error = err.Error()
			} else {
				s.Latest, s.Update = latest, compareVersions(latest, s.Current) > 0
			}
			mu.Lock()
			out[name] = s
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// Update 安装 PyPI 最新稳定版；失败不改变当前引擎。
func (u *Updater) Update(ctx context.Context, name string) (string, error) {
	spec, ok := updatableEngines[name]
	if !ok {
		return "", fmt.Errorf("引擎不支持在线更新：%s", name)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	version, err := latestVersion(ctx, name)
	if err != nil {
		return "", err
	}
	if compareVersions(version, engineVersion(ctx, name, "keep")) <= 0 {
		return version, nil
	}
	folder := name + "-" + version
	target := filepath.Join(u.dir, folder)
	_ = os.RemoveAll(target)
	if err = os.MkdirAll(u.dir, 0755); err != nil {
		return "", err
	}
	args := []string{"install", "--no-cache-dir", "--disable-pip-version-check", "--target", target, spec + "==" + version}
	if output, runErr := exec.CommandContext(ctx, "pip", args...).CombinedOutput(); runErr != nil {
		_ = os.RemoveAll(target)
		return "", fmt.Errorf("安装 %s %s 失败：%w\n%s", name, version, runErr, tail(string(output), 600))
	}
	previous := u.manifest[name]
	u.manifest[name] = folder
	u.save()
	u.activate()
	// 新目录必须能运行；否则回滚到原来的目录。
	if got := engineVersion(ctx, name, "keep"); compareVersions(got, version) != 0 {
		if previous != "" {
			u.manifest[name] = previous
		} else {
			delete(u.manifest, name)
		}
		u.save()
		u.activate()
		_ = os.RemoveAll(target)
		return "", fmt.Errorf("更新后的 %s 无法运行（读取到 %q），已恢复原版本", name, got)
	}
	if previous != "" && previous != folder {
		_ = os.RemoveAll(filepath.Join(u.dir, previous))
	}
	return version, nil
}

// engineVersion 的 keep 保留当前 PYTHONPATH；空值用于测量镜像内置版本。
func engineVersion(ctx context.Context, name, mode string) string {
	cmd := exec.CommandContext(ctx, name, "--version")
	if mode == "" {
		env := os.Environ()[:0:0]
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "PYTHONPATH=") {
				env = append(env, item)
			}
		}
		cmd.Env = env
	}
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
}

func latestVersion(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://pypi.org/pypi/"+name+"/json", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询 PyPI 失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("查询 PyPI 失败：HTTP %d", response.StatusCode)
	}
	var body struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil || body.Info.Version == "" {
		return "", fmt.Errorf("PyPI 响应无效")
	}
	return body.Info.Version, nil
}

// compareVersions 按数字段比较，忽略前导零（2026.08.19 与 2026.8.19 相同）。
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
