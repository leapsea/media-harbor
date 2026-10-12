// Package service 编排任务和文件生命周期。
package service

import (
	"fmt"
	"log/slog"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DirectoryStatus 将挂载状态与实际进程可写状态独立呈现。
type DirectoryStatus struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Mounted   string    `json:"mounted"`
	Writable  bool      `json:"writable"`
	Error     string    `json:"error"`
	CheckedAt time.Time `json:"checked_at"`
	UID       int       `json:"uid"`
	GID       int       `json:"gid"`
}

// DetectDirectories 以应用当前 UID/GID 创建并清理测试文件。
// 探测不使用 root 代替应用用户。
// 专用测试文件在探测完成后立即移除。
func DetectDirectories(c config.Config) []DirectoryStatus {
	out := []DirectoryStatus{}
	for _, v := range []struct{ name, path string }{{"运行数据", c.DataDir}, {"下载数据", c.DownloadDir}, {"归档数据", c.ArchiveDir}} {
		path, _ := filepath.Abs(v.path)
		s := DirectoryStatus{Name: v.name, Path: path, Mounted: mountState(path), CheckedAt: time.Now(), UID: os.Getuid(), GID: os.Getgid()}
		e := os.MkdirAll(path, 0755)
		if e == nil {
			var f *os.File
			f, e = os.CreateTemp(path, ".media-probe-*")
			if e == nil {
				_, e = f.WriteString("probe")
				closeErr := f.Close()
				if e == nil {
					e = closeErr
				}
				if e == nil {
					_, e = os.ReadFile(f.Name())
				}
				removeErr := os.Remove(f.Name())
				if e == nil {
					e = removeErr
				}
			}
		}
		s.Writable = e == nil
		if e != nil {
			s.Error = e.Error()
		}
		slog.Info("目录检测", "path", s.Path, "mounted", s.Mounted, "writable", s.Writable, "uid", s.UID, "gid", s.GID, "error", s.Error)
		out = append(out, s)
	}
	return out
}

// Linux 的 mountinfo 可识别 Docker bind mount；其他平台保留 Unknown。
// 精确匹配配置目录的挂载点，存在且可写不等于已挂载。
// 无法读取挂载信息时不猜测，返回 unknown。
func mountState(path string) string {
	if runtime.GOOS != "linux" {
		return "unknown"
	}
	raw, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return "unknown"
	}
	path, e = filepath.EvalSymlinks(path)
	if e != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 {
			p := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, "\\").Replace(fields[4])
			if filepath.Clean(p) == path {
				return "mounted"
			}
		}
	}
	return "unmounted"
}

// InitialSettings 在首次建库时根据归档挂载设置新任务默认值。
// 默认勾选与挂载关联，实际归档仍检查目录写入能力。
func InitialSettings(c config.Config) domain.Settings {
	settings := config.Defaults()
	path, _ := filepath.Abs(c.ArchiveDir)
	settings.AutoArchive = mountState(path) == "mounted"
	return settings
}

// ArchiveMounted 只表达归档卷挂载状态，用于设置开关的启用条件。
func ArchiveMounted(dirs []DirectoryStatus) bool {
	return len(dirs) == 3 && dirs[2].Mounted == "mounted"
}

// ArchiveAvailable 仅在归档卷已经确认挂载且可写时开放。
// 下载允许内部目录，归档要求独立的持久挂载。
func ArchiveAvailable(dirs []DirectoryStatus) bool {
	return ArchiveMounted(dirs) && dirs[2].Writable
}

// directoryError 为 Dashboard 的可操作状态提供简短原因。
func directoryError(dirs []DirectoryStatus) string {
	if ArchiveAvailable(dirs) {
		return ""
	}
	return fmt.Sprint("归档目录未确认挂载或不可写")
}
