package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"media-harbor/internal/domain"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 同一博主的多个配置可能并行扫描，文件比较与替换必须作为一个操作。
var avatarMu sync.Mutex

// AvatarPath 优先使用平台稳定 ID，同一作者的主页与详情共用头像。
func AvatarPath(root string, sub domain.Subscription) string {
	if sub.Platform != "instagram" || sub.StableID == "" {
		return ""
	}
	key := sha256.Sum256([]byte(sub.StableID))
	return filepath.Join(root, "avatars", "instagram", fmt.Sprintf("%x.img", key))
}

// UpdateAvatar 复用 gallery-dl 的头像提取器和 Cookie；不受媒体历史归档去重影响。
// 先下载到独立目录，只有完整文件大小变化才替换，失败保留原头像。
func (c CLI) UpdateAvatar(ctx context.Context, sub domain.Subscription, path string, log func(string)) error {
	if path == "" || sub.CurrentName == "" {
		return nil
	}
	started := time.Now()
	log(fmt.Sprintf("头像阶段：博主 %s；稳定 ID=%s；开始下载", sub.CurrentName, sub.StableID))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(filepath.Dir(path), ".avatar-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	args := append(c.common(sub), "--directory", dir, "--filename", "avatar.{extension}", "--range", "1",
		"-o", "extractor.instagram.avatar.archive=null", "-o", "extractor.instagram.avatar.postprocessors=[]",
		"--", "https://www.instagram.com/"+url.PathEscape(sub.CurrentName)+"/avatar/")
	if _, err = run(ctx, "gallery-dl", args, log, false); err != nil {
		return fmt.Errorf("下载博主 %s 头像失败：%w", sub.CurrentName, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasPrefix(file.Name(), "avatar.") || strings.HasSuffix(file.Name(), ".part") {
			continue
		}
		info, err := file.Info()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			continue
		}
		return replaceAvatar(path, filepath.Join(dir, file.Name()), info.Size(), sub.CurrentName, started, log)
	}
	return fmt.Errorf("博主 %s 的头像提取未生成文件", sub.CurrentName)
}

// replaceAvatar 比较实际下载字节数，大小一致时不改变原文件内容或修改时间。
func replaceAvatar(path, downloaded string, size int64, name string, started time.Time, log func(string)) error {
	avatarMu.Lock()
	defer avatarMu.Unlock()
	if old, err := os.Stat(path); err == nil && old.Size() == size {
		log(fmt.Sprintf("头像阶段：博主 %s；大小 %d 字节一致，保留原头像；耗时 %s", name, size, time.Since(started).Round(time.Millisecond)))
		return nil
	}
	if err := os.Rename(downloaded, path); err != nil {
		return err
	}
	log(fmt.Sprintf("头像阶段：博主 %s；已保存 %d 字节；耗时 %s", name, size, time.Since(started).Round(time.Millisecond)))
	return nil
}
