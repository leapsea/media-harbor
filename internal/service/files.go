package service

import (
	"encoding/json"
	"fmt"
	"io"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// renderPathAt 按本地时区格式化选定的目录日期。
// filename 保留引擎产物扩展名，目录模板变更不移动历史文件。
func renderPathAt(template string, s domain.Subscription, c domain.Content, filename string, date time.Time) string {
	date = date.Local()
	year, month, day := date.Format("2006"), date.Format("01"), date.Format("02")
	return strings.NewReplacer("{platform}", s.Platform, "{subscription_id}", strconv.FormatInt(s.ID, 10), "{directory_name}", s.DisplayName, "{current_name}", s.CurrentName, "{year}", year, "{month}", month, "{day}", day, "{content_id}", c.RemoteID, "{filename}", filename, "{ext}", strings.TrimPrefix(filepath.Ext(filename), ".")).Replace(template)
}

// moveFile 优先重命名，跨文件系统复制成功后再移除源文件。
// 同盘移动不额外保留副本。
// 复制或删除失败直接返回，历史页面提供归档重试。
func moveFile(src, dst string) error {
	if src == dst {
		return nil
	}
	if e := os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
		return e
	}
	if e := os.Rename(src, dst); e == nil {
		return nil
	}
	if e := copyFile(src, dst); e != nil {
		return e
	}
	return os.Remove(src)
}

// taskFiles 排除工作目录内部元数据和未完成文件。
// 遍历支持引擎附加产物与字幕文件。
// 隐藏元数据仅供应用内部日期解析使用。
func taskFiles(dir string) ([]string, error) {
	files := []string{}
	e := filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if !engine.CompletedArtifact(path) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if e != nil {
		return nil, e
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("引擎没有生成可保存的文件，请检查下载选项和任务日志")
	}
	return files, nil
}

// placeFiles 逐个保存位置，归位被打断时已有文件仍可追踪。
// 每完成一个文件就更新历史，后续文件失败仍能找到已归位产物。
// 订阅模板为空才沿用系统模板。
func (a *App) placeFiles(s domain.Subscription, c *domain.Content, dir string) error {
	return a.placeFilesMode(s, c, dir, false)
}

// placeFilesMode 全量覆盖同类原文件，标题变更不会在原目录留下第二份视频。
// 原位置来自文件索引，归档中的产物也在原位置更新，不额外复制回下载卷。
func (a *App) placeFilesMode(s domain.Subscription, c *domain.Content, dir string, overwrite bool) error {
	files, e := taskFiles(dir)
	if e != nil {
		return e
	}
	settings, e := a.Repo.Settings()
	if e != nil {
		return e
	}
	template := s.Template
	if template == "" {
		template = settings.Template
	}
	if c.Published.IsZero() {
		c.Published = engine.PublicationDate(dir)
	}
	date := s.CreatedAt
	if settings.UseUploadDate && !c.Published.IsZero() {
		date = c.Published
	}
	for _, src := range files {
		tags := fileTags(src, s.Platform)
		dst := filepath.Join(a.Config.DownloadDir, renderPathAt(template, s, *c, filepath.Base(src), date))
		// 已有产物所在目录保持不变，元数据覆盖同一个索引文件，不复制已下载媒体。
		for _, old := range c.Paths {
			if overwrite && sameArtifactSlot(src, old, s.Platform) {
				dst = old
				break
			}
			if strings.HasSuffix(src, ".info.json") && strings.HasSuffix(old, ".info.json") {
				dst = old
				break
			}
			if engine.CompletedArtifact(old) {
				dst = filepath.Join(filepath.Dir(old), filepath.Base(src))
			}
		}
		if e = moveFile(src, dst); e != nil {
			return e
		}
		c.Paths = appendUnique(c.Paths, dst)
		for _, tag := range tags {
			c.Tags = appendUnique(c.Tags, tag)
		}
		enriched := []domain.Content{*c}
		enrichHistory(enriched, []domain.Subscription{s})
		*c = enriched[0]
		if e = a.Repo.SaveContent(c); e != nil {
			return e
		}
	}
	// 文件标签重新由当前实际文件派生，元数据更新后不残留旧评论标签。
	c.Tags = nil
	c.Archived = len(c.Paths) > 0
	for _, path := range c.Paths {
		if engine.CompletedArtifact(path) {
			for _, tag := range fileTags(path, s.Platform) {
				c.Tags = appendUnique(c.Tags, tag)
			}
		}
		rel, err := filepath.Rel(a.Config.ArchiveDir, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			c.Archived = false
		}
	}
	return a.Repo.SaveContent(c)
}

// sameArtifactSlot 只覆盖同扩展名与产物类型，音频提取及各语言字幕分别匹配。
// 格式变化仍使用引擎的新扩展名，不把MP4字节写入不同格式后缀。
func sameArtifactSlot(src, old, platform string) bool {
	if filepath.Ext(src) != filepath.Ext(old) {
		return false
	}
	a, b := fileTags(src, platform), fileTags(old, platform)
	if len(a) == 0 || len(b) == 0 || a[0] != b[0] {
		return false
	}
	if a[0] == "audio" {
		return strings.Contains(src, ".extracted.") == strings.Contains(old, ".extracted.")
	}
	if a[0] == "subtitles" {
		language := func(path string) string {
			stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if pos := strings.LastIndex(stem, "."); pos >= 0 {
				return stem[pos:]
			}
			return ""
		}
		return language(src) == language(old)
	}
	return true
}

// archive 保留配置和待归档记录，不可用时由调度器延后处理。
// 文件列表可能同时包含已归档和未归档的路径。
// 重试时跳过已经离开下载根目录的记录。
func (a *App) archive(c *domain.Content) error {
	// 头像由作者共享，但归档只随实际媒体记录触发，不扫描整个 data 目录。
	sub, err := a.Repo.Subscription(c.SubscriptionID)
	if err != nil {
		return err
	}
	if err = a.archiveAvatar(sub); err != nil {
		return fmt.Errorf("头像归档失败：%w", err)
	}
	for i, src := range c.Paths {
		rel, e := filepath.Rel(a.Config.DownloadDir, src)
		if e != nil {
			return e
		}
		if strings.HasPrefix(rel, "..") {
			continue
		}
		dst := filepath.Join(a.Config.ArchiveDir, rel)
		if e = moveFile(src, dst); e != nil {
			return e
		}
		c.Paths[i] = dst
		enriched := []domain.Content{*c}
		enrichHistory(enriched, []domain.Subscription{sub})
		*c = enriched[0]
		if e = a.Repo.SaveContent(c); e != nil {
			return e
		}
	}
	c.Archived = true
	return a.Repo.SaveContent(c)
}

// appendUnique 避免重试归位时在同一历史中重复保存路径。
func appendUnique(v []string, s string) []string {
	for _, p := range v {
		if p == s {
			return v
		}
	}
	return append(v, s)
}

// copyFile 是跨文件系统移动的基础复制操作，调用者随后删除源文件。
// 复制完成和目标关闭成功后，移动操作才删除源文件。
func copyFile(src, dst string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.Create(dst)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	closeErr := out.Close()
	if e != nil {
		return e
	}
	return closeErr
}

// fileTags 根据真实产物分类，评论由 info JSON 内的评论字段确认。
// 勾选下载选项不意味着平台一定提供字幕或评论。
func fileTags(path, platform string) []string {
	// 原始音轨容器可以与视频相同，先用专属产物标识判断音频。
	name := filepath.Base(path)
	if strings.Contains(name, ".audio.") || strings.Contains(name, ".extracted.") {
		return []string{"audio"}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".webm", ".mkv", ".mov", ".avi":
		return []string{"video"}
	case ".m4a", ".mp3", ".flac", ".aac", ".opus", ".ogg", ".wav", ".mka", ".ac3", ".eac3":
		return []string{"audio"}
	case ".srt", ".vtt", ".ass", ".ssa", ".ttml", ".srv1", ".srv2", ".srv3", ".json3", ".lrc":
		return []string{"subtitles"}
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		if platform == "instagram" {
			return []string{"image"}
		}
		return []string{"thumbnail"}
	case ".json":
		if !strings.HasSuffix(path, ".info.json") {
			return nil
		}
		return infoFileDetails(path).Tags
	}
	return nil
}

// infoFileDetails 一次读取 JSON，标签与数量都来自同一个实际文件。
// 空数组表示已保存零条评论；缺失或 null 不表示已获取评论。
func infoFileDetails(path string) domain.ContentFile {
	file := domain.ContentFile{Path: path, Tags: []string{"metadata"}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return file
	}
	var info struct {
		Comments []json.RawMessage `json:"comments"`
	}
	if json.Unmarshal(raw, &info) == nil && info.Comments != nil {
		count := len(info.Comments)
		file.Tags = append(file.Tags, "comments")
		file.CommentCount = &count
	}
	return file
}
