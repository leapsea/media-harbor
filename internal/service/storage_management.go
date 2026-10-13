package service

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// StorageUsage 描述任务日志与工作缓存，业务下载文件不参与清理。
type StorageUsage struct {
	Path                 string `json:"path"`
	Bytes                int64  `json:"bytes"`
	Files                int    `json:"files"`
	CleanableBytes       int64  `json:"cleanable_bytes"`
	CleanableDirectories int    `json:"cleanable_directories"`
	RetainedDirectories  int    `json:"retained_directories"`
}
type StorageStatus struct {
	Logs StorageUsage `json:"logs"`
	Work StorageUsage `json:"work"`
}

// directoryUsage 不跟随符号链接，统计仅限当前存储目录。
func directoryUsage(path string) (StorageUsage, error) {
	result := StorageUsage{Path: path}
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result.Files++
		result.Bytes += info.Size()
		return nil
	})
	return result, err
}

// retainedWork 键直接来自当前任务身份，失败或取消缓存仍可用于显式重试。
func (a *App) retainedWork() (map[string]bool, error) {
	tasks, err := a.Repo.Tasks()
	if err != nil {
		return nil, err
	}
	retained := map[string]bool{}
	for _, task := range tasks {
		if task.ContentID != 0 && (task.Status != "completed" || a.active[task.ID] != nil) {
			retained[taskKey(task)] = true
		}
	}
	return retained, nil
}

// StorageStatus 按需读取占用，避免每轮 Dashboard 刷新扫描文件系统。
func (a *App) StorageStatus() (StorageStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	retained, err := a.retainedWork()
	if err != nil {
		return StorageStatus{}, err
	}
	logs, err := directoryUsage(a.Config.LogDir)
	if err != nil {
		return StorageStatus{}, err
	}
	root := filepath.Join(a.Config.DownloadDir, ".work")
	work, err := directoryUsage(root)
	if err != nil {
		return StorageStatus{}, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return StorageStatus{Logs: logs, Work: work}, nil
	}
	if err != nil {
		return StorageStatus{}, err
	}
	for _, entry := range entries {
		if retained[entry.Name()] {
			work.RetainedDirectories++
			continue
		}
		usage, err := directoryUsage(filepath.Join(root, entry.Name()))
		if err != nil {
			return StorageStatus{}, err
		}
		work.CleanableDirectories++
		work.CleanableBytes += usage.Bytes
	}
	return StorageStatus{Logs: logs, Work: work}, nil
}

// ClearLogs 与日志写入共用互斥锁；活动文件截断后仍由原描述符继续追加。
// 无活动写入的文件直接删除，不更改任务状态或下载索引。
func (a *App) ClearLogs() (int, error) {
	a.logMu.Lock()
	defer a.logMu.Unlock()
	entries, err := os.ReadDir(a.Config.LogDir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		path := filepath.Join(a.Config.LogDir, entry.Name())
		if writer := a.loggers[path]; writer != nil {
			if err = writer.buffer.Flush(); err == nil {
				err = os.Truncate(path, 0)
			}
			if err == nil {
				_, err = writer.file.Seek(0, io.SeekStart)
			}
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// CleanWork 只删除无关联及已完成任务残留，保留可继续执行或重试的缓存。
// 与认领、完成持锁互斥，防止清理过程中目录重新被任务使用。
func (a *App) CleanWork() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	retained, err := a.retainedWork()
	if err != nil {
		return 0, err
	}
	root := filepath.Join(a.Config.DownloadDir, ".work")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if retained[entry.Name()] {
			continue
		}
		if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
