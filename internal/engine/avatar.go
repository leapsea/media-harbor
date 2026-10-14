package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"net/http"
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
	base := filepath.Join(root, "avatars", "instagram", fmt.Sprintf("%x", key))
	// 真实格式优先，旧 img 仅作迁移失败时的读取兼容；新头像默认目标为 jpg。
	for _, ext := range avatarExtensions {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext
		}
	}
	return base + ".jpg"
	// 历史资产：return filepath.Join(root, "avatars", "instagram", fmt.Sprintf("%x.img", key))。
}

// 允许原 SDK 保存的常见图片格式，固定作者身份只决定文件名主体。
var avatarExtensions = []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".bmp", ".tiff", ".ico", ".img"}

// avatarExtension 优先识别字节内容，避免 CDN 或 SDK 给出的后缀与实际格式不符。
func avatarExtension(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var header [config.AvatarHeaderBytes]byte
	n, err := file.Read(header[:])
	if err != nil && err != io.EOF {
		return "", err
	}
	switch http.DetectContentType(header[:n]) {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/webp":
		return ".webp", nil
	case "image/gif":
		return ".gif", nil
	case "image/bmp":
		return ".bmp", nil
	case "image/tiff":
		return ".tiff", nil
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico", nil
	}
	// 非标准格式沿用 SDK 扩展名；无法识别的旧文件保持可读，不擅自转换内容。
	ext := strings.ToLower(filepath.Ext(path))
	for _, known := range avatarExtensions {
		if ext == known {
			if ext == ".jpeg" {
				ext = ".jpg"
			}
			return ext, nil
		}
	}
	return ".img", nil
}

// NormalizeAvatar 只更正文件名，原内容及修改时间都由同目录 rename 保留。
func NormalizeAvatar(path string) (string, error) {
	avatarMu.Lock()
	defer avatarMu.Unlock()
	return normalizeAvatar(path)
}

// normalizeAvatar 在头像锁内执行；正确后缀已存在时优先保留现有文件。
func normalizeAvatar(path string) (string, error) {
	ext, err := avatarExtension(path)
	if err != nil {
		return "", err
	}
	destination := strings.TrimSuffix(path, filepath.Ext(path)) + ext
	if destination == path {
		return path, nil
	}
	if _, err := os.Stat(destination); err == nil {
		return destination, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(path, destination); err != nil {
		return "", err
	}
	return destination, nil
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
	stage, script := "avatar", "import sys,gallery_dl\nsys.exit(gallery_dl.main())\n"
	if c.pipeline("instagram") != nil {
		// 先串行解析头像地址，再把原 SDK 下载交给消费者。
		avatarSub := sub
		avatarSub.URL = "https://www.instagram.com/" + url.PathEscape(sub.CurrentName) + "/avatar/"
		var source domain.MediaSource
		_, err = c.Scan(ctx, avatarSub, 1, log, func(batch domain.ScanResult) error {
			for _, candidate := range batch.MediaSources {
				source = candidate
				break
			}
			return nil
		})
		if err != nil {
			return err
		}
		if source.URL == "" {
			return fmt.Errorf("博主 %s 的头像提取未返回媒体地址", sub.CurrentName)
		}
		// 只写入本次临时目录，沿用原 Cookie、文件比较与稳定路径语义。
		raw, err := json.Marshal(struct {
			domain.MediaSource
			PostURL string `json:"post_url"`
		}{source, avatarSub.URL})
		if err != nil {
			return err
		}
		path := filepath.Join(dir, ".source.json")
		if err = os.WriteFile(path, raw, 0600); err != nil {
			return err
		}
		args = append([]string{path}, args...)
		stage, script = "media", instagramCached
	}
	env, end, err := c.instagramController().begin(ctx, stage, log)
	if err != nil {
		return err
	}
	defer end()
	release, err := c.consumer(ctx, "instagram", log)
	if err != nil {
		return err
	}
	defer release()
	args = append([]string{"-u", "-c", instagramScript(script)}, args...)
	if _, err = runWithEnv(ctx, "python3", args, log, false, env); err != nil {
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
		// 只选图片文件，后处理生成的 avatar.json 等元数据不能成为头像。
		image := false
		for _, ext := range avatarExtensions {
			if strings.ToLower(filepath.Ext(file.Name())) == ext {
				image = true
				break
			}
		}
		if !image {
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
	if _, err := os.Stat(path); err == nil {
		var err error
		path, err = normalizeAvatar(path)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if old, err := os.Stat(path); err == nil && old.Size() == size {
		log(fmt.Sprintf("头像阶段：博主 %s；大小 %d 字节一致，保留原头像；耗时 %s", name, size, time.Since(started).Round(time.Millisecond)))
		return nil
	}
	ext, err := avatarExtension(downloaded)
	if err != nil {
		return err
	}
	destination := strings.TrimSuffix(path, filepath.Ext(path)) + ext
	if err := os.Rename(downloaded, destination); err != nil {
		return err
	}
	// 格式变化时移除被替换的旧路径，避免查找时仍选中旧格式的头像。
	if destination != path {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	log(fmt.Sprintf("头像阶段：博主 %s；已保存 %d 字节；格式 %s；耗时 %s", name, size, ext, time.Since(started).Round(time.Millisecond)))
	// 历史资产：os.Rename(downloaded, path) 将 SDK 图片固定保存为 img。
	return nil
}
