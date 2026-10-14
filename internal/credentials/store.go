// Package credentials 管理平台Cookie状态，停用记录与原始凭据分开保存。
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable 表示平台暂停条件，任务应留在队列而非自动匿名重试。
var ErrUnavailable = errors.New("平台没有可用 Cookie")

// Status 不公开Cookie原文；配置存在与可用于请求是两个独立状态。
type Status struct {
	Configured      bool      `json:"configured"`
	Usable          bool      `json:"usable"`
	PlatformEnabled bool      `json:"platform_enabled"`
	State           string    `json:"state"`
	Reason          string    `json:"reason,omitempty"`
	DisabledAt      time.Time `json:"disabled_at,omitempty"`
	Revision        int64     `json:"revision"`
}

// Store 的锁同时保护编辑与停用，版本号防止旧SDK停用新上传的凭据。
// 原始Cookie和状态均位于已有data挂载，不新增账号副本或外部存储。
type Store struct {
	mu     sync.Mutex
	root   string
	active bool
}

// Cookie 是服务器返回的单条更新，仅在内部控制通道传递，不进入日志或状态API。
type Cookie struct {
	Name, Value, Domain, Path string
	Expires                   int64
	Secure, HTTPOnly          bool
}

// Apply 按当前版本合并服务器更新；旧进程不能覆盖新保存或已停用的凭据。
func (s *Store) Apply(p string, revision int64, updates []Cookie) error {
	// 无服务器更新时不重写文件，保留用户导入的原文与时间。
	if len(updates) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.status(p)
	if err != nil {
		return err
	}
	// 停用即阻止后续响应更新，防止并发会话把已停用凭据重新激活。
	if !state.Usable || state.Revision != revision {
		return nil
	}
	raw, err := os.ReadFile(s.Path(p))
	if err != nil {
		return err
	}
	// 按域名、路径和名称定位Cookie，避免误合并其他路径的同名字段。
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for _, c := range updates {
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain != p+".com" && !strings.HasSuffix(domain, "."+p+".com") {
			continue
		}
		if c.Path == "" {
			c.Path = "/"
		}
		// Netscape的域标志与HttpOnly前缀沿用下载SDK可识别的格式。
		rowDomain := c.Domain
		include := "FALSE"
		secure := "FALSE"
		if strings.HasPrefix(c.Domain, ".") {
			include = "TRUE"
		}
		if c.Secure {
			secure = "TRUE"
		}
		if c.HTTPOnly {
			rowDomain = "#HttpOnly_" + rowDomain
		}
		row := strings.Join([]string{rowDomain, include, c.Path, secure, strconv.FormatInt(max(0, c.Expires), 10), c.Name, c.Value}, "\t")
		found := false
		for i, line := range lines {
			fields := strings.Split(strings.TrimPrefix(line, "#HttpOnly_"), "\t")
			if len(fields) == 7 && fields[0] == c.Domain && fields[2] == c.Path && fields[5] == c.Name {
				lines[i] = row
				found = true
				break
			}
		}
		if !found {
			lines = append(lines, row)
		}
	}
	return atomicWrite(s.Path(p), []byte(strings.Join(lines, "\n")+"\n"))
}

// New 只建立路径视图，不读取或打印凭据。
func New(root string) *Store { return &Store{root: filepath.Join(root, "cookies")} }

// Activate 由服务启用认证规则，独立离线工具保留原测试协议。
func (s *Store) Activate()                   { s.mu.Lock(); s.active = true; s.mu.Unlock() }
func (s *Store) Active() bool                { s.mu.Lock(); defer s.mu.Unlock(); return s.active }
func (s *Store) Path(platform string) string { return filepath.Join(s.root, platform+".txt") }
func validPlatform(p string) bool            { return p == "instagram" || p == "youtube" }

// state 只记录应用判断，不把Cookie值或平台响应写入文件。
func (s *Store) state(p string) (Status, error) {
	// 没有状态文件的旧配置从未验证开始，现有内容不自动宣称已验证。
	v := Status{Revision: 1, State: "unverified"}
	b, err := os.ReadFile(filepath.Join(s.root, p+".state.json"))
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	return v, nil
}

// Status 在本地到期或缺少登录字段时停用，不把“文件存在”当成远端验证成功。
func (s *Store) Status(p string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status(p)
}
func (s *Store) status(p string) (Status, error) {
	if !validPlatform(p) {
		return Status{}, fmt.Errorf("平台无效")
	}
	v, err := s.state(p)
	if err != nil {
		return v, err
	}
	b, err := os.ReadFile(s.Path(p))
	if os.IsNotExist(err) {
		v.Configured = false
		v.State = "missing"
		v.Reason = "未配置 Cookie"
		v.Usable = false
		v.PlatformEnabled = p == "youtube"
		return v, nil
	}
	if err != nil {
		return v, err
	}
	// 文件存在仅证明已配置；停用记录优先于文件里仍保留的Cookie。
	v.Configured = true
	if v.State == "missing" {
		v.State = "unverified"
	}
	if v.State == "disabled" {
		v.Usable = false
		v.PlatformEnabled = p == "youtube"
		return v, nil
	}
	if reason := cookieReason(p, string(b), time.Now()); reason != "" {
		v.State = "disabled"
		v.Reason = reason
		v.Usable = false
		v.PlatformEnabled = p == "youtube"
		return v, nil
	}
	// 本地结构检查通过后允许验证请求，远端成功响应再转为valid。
	v.Usable = true
	v.PlatformEnabled = true
	return v, nil
}

// Check 在启动及每次实际请求前执行；停用后不降级成匿名请求。
func (s *Store) Check(p string) error {
	v, err := s.Status(p)
	if err != nil {
		return err
	}
	if !v.PlatformEnabled {
		return fmt.Errorf("%w：%s；%s", ErrUnavailable, p, v.Reason)
	}
	return nil
}

// Save 保留原格式，新版本解除历史停用但仍标记为未验证。
func (s *Store) Save(p, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validPlatform(p) {
		return fmt.Errorf("平台无效")
	}
	v, err := s.state(p)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	// 原文替换与状态更新都在锁内，SDK回写采用同一把锁和版本检查。
	if err = atomicWrite(s.Path(p), []byte(content)); err != nil {
		return err
	}
	return s.writeState(p, Status{Revision: v.Revision + 1, State: "unverified"})
}

// Delete 清除用户选中的配置，平台因此暂停，现有任务仍由服务管理。
func (s *Store) Delete(p string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validPlatform(p) {
		return fmt.Errorf("平台无效")
	}
	v, err := s.state(p)
	if err != nil {
		return err
	}
	if err = os.Remove(s.Path(p)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	// 删除也推进版本，之前的SDK响应不能恢复已经删除的凭据。
	return s.writeState(p, Status{Revision: v.Revision + 1, State: "missing"})
}

// Disable 只接受当前版本的认证拒绝；新Cookie不能被旧进程错误覆盖。
func (s *Store) Disable(p string, revision int64, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.status(p)
	if err != nil {
		return false, err
	}
	if !v.Configured || v.Revision != revision || v.State == "disabled" {
		return false, nil
	}
	// 固定原因由认证分类器产生，不持久保存错误里的URL或响应原文。
	v.State = "disabled"
	v.Usable = false
	v.PlatformEnabled = p == "youtube"
	v.Reason = reason
	v.DisabledAt = time.Now()
	return true, s.writeState(p, v)
}

// Confirm 只有携带凭据的成功平台响应才能证明可用，CDN成功不算登录验证。
func (s *Store) Confirm(p string, revision int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.status(p)
	if err != nil {
		return err
	}
	// 认证成功不得解除已停用的状态；只有用户保存新版本才能恢复。
	if !v.Usable || v.Revision != revision || v.State == "valid" {
		return nil
	}
	v.State = "valid"
	return s.writeState(p, v)
}
func (s *Store) writeState(p string, v Status) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.root, p+".state.json"), b)
}

// 临时文件与目标位于同一目录，读者只看到完整版本。
func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".cookie-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	// 写入失败不替换目标，临时文件在所有退出路径上清理。
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

// cookieReason 仅判定登录Cookie和本地期限，其他追踪Cookie过期不会停用账号。
func cookieReason(p, content string, now time.Time) string {
	values := map[string]bool{}
	expired := false
	for _, line := range strings.Split(content, "\n") {
		// 浏览器导出的HttpOnly记录仍需参与登录字段的到期判断。
		line = strings.TrimPrefix(strings.TrimSpace(line), "#HttpOnly_")
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 7 {
			continue
		}
		domain := strings.TrimPrefix(parts[0], ".")
		if domain != p+".com" && !strings.HasSuffix(domain, "."+p+".com") {
			continue
		}
		name := parts[5]
		relevant := p == "instagram" && name == "sessionid" || p == "youtube" && (name == "SAPISID" || name == "__Secure-1PAPISID" || name == "__Secure-3PAPISID" || name == "LOGIN_INFO")
		if !relevant || parts[6] == "" {
			continue
		}
		// 到期为0是会话Cookie，真实有效性必须由平台响应判断。
		expires, err := strconv.ParseInt(parts[4], 10, 64)
		if err != nil {
			continue
		}
		if expires > 0 && expires <= now.Unix() {
			expired = true
			continue
		}
		values[name] = true
	}
	// YouTube登录字段与当前SDK认证检查一致，其他偏好Cookie不算登录。
	if p == "instagram" && values["sessionid"] || p == "youtube" && values["LOGIN_INFO"] && (values["SAPISID"] || values["__Secure-1PAPISID"] || values["__Secure-3PAPISID"]) {
		return ""
	}
	if expired {
		return "登录 Cookie 已过期"
	}
	return "缺少有效的登录 Cookie"
}

// FailureReason 只匹配明确认证失败，限流、私有内容权限和签名地址过期不在此列。
func FailureReason(p, message string) string {
	text := strings.ToLower(message)
	if p == "instagram" {
		for _, mark := range []string{"redirect to login page", "/accounts/login", "login_required", "authenticationerror", "authentication failed", "authrequired"} {
			if strings.Contains(text, mark) {
				return "Instagram 登录会话失效"
			}
		}
		if strings.Contains(text, "redirect to challenge page") {
			return "Instagram 登录需要验证"
		}
	}
	// 年龄、私人视频或机器人确认提示均不等于已提供的Cookie失效。
	if p == "youtube" && strings.Contains(text, "provided youtube account cookies are no longer valid") {
		return "YouTube 登录 Cookie 已失效"
	}
	return ""
}
