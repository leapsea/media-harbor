package service

import (
	"context"
	"fmt"
	"log/slog"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"os"
	"path/filepath"
	"strings"
)

// SubscriptionView 为任务管理附加本地头像地址，不改变订阅的持久化结构。
type SubscriptionView struct {
	domain.Subscription
	AvatarURL string `json:"avatar_url,omitempty"`
}

// SubscriptionViews 从已有文件构造展示信息；文件大小变化时刷新浏览器地址。
func (a *App) SubscriptionViews() ([]SubscriptionView, error) {
	subs, err := a.Repo.Subscriptions()
	if err != nil {
		return nil, err
	}
	views := make([]SubscriptionView, 0, len(subs))
	for _, sub := range subs {
		view := SubscriptionView{Subscription: sub}
		if path, err := a.avatarFile(sub); err == nil {
			info, err := os.Stat(path)
			if err != nil {
				views = append(views, view)
				continue
			}
			view.AvatarURL = fmt.Sprintf("/api/subscriptions/%d/avatar?v=%d", sub.ID, info.Size())
		}
		views = append(views, view)
	}
	return views, nil
}

// AvatarFile 按当前订阅的博主身份读取共享头像，不接受客户端文件路径。
func (a *App) AvatarFile(id int64) (string, error) {
	sub, err := a.Repo.Subscription(id)
	if err != nil {
		return "", err
	}
	return a.avatarFile(sub)
}

// avatarFile 只查找下载与归档根目录，不读取或迁移 data 中的旧头像。
func (a *App) avatarFile(sub domain.Subscription) (string, error) {
	for _, root := range []string{a.Config.DownloadDir, a.Config.ArchiveDir} {
		path := engine.AvatarPath(root, sub)
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
	}
	return "", os.ErrNotExist
}

// updateCreationAvatar 在任务创建解析阶段更新共享头像，失败只记录到任务配置日志。
// 已归档头像在原位置更新，同大小不覆盖，不额外复制一份到下载目录。
func (a *App) updateCreationAvatar(ctx context.Context, sub domain.Subscription, log func(string)) {
	downloader, ok := a.Engine.(domain.AvatarDownloader)
	if !ok || sub.Platform != "instagram" || sub.StableID == "" {
		return
	}
	a.avatarMu.Lock()
	defer a.avatarMu.Unlock()
	path, err := a.avatarFile(sub)
	if os.IsNotExist(err) {
		path = engine.AvatarPath(a.Config.DownloadDir, sub)
	} else if err != nil {
		log(fmt.Sprintf("头像阶段：博主 %s；读取失败：%v", sub.CurrentName, err))
		return
	}
	if err = downloader.UpdateAvatar(ctx, sub, path, log); err != nil {
		log(fmt.Sprintf("头像阶段：博主 %s；错误：%v", sub.CurrentName, err))
	}
}

// archiveAvatar 与媒体归档共用移动策略；没有头像的任务无需创建空文件。
// 归档卷恢复后，原有待归档媒体会再次触发这一阶段。
func (a *App) archiveAvatar(sub domain.Subscription) error {
	a.avatarMu.Lock()
	defer a.avatarMu.Unlock()
	src := engine.AvatarPath(a.Config.DownloadDir, sub)
	if src == "" {
		return nil
	}
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	// 归档是移动原图，必须保留来源扩展名，不能回落到默认 jpg。
	destination := engine.AvatarPath(a.Config.ArchiveDir, sub)
	destination = strings.TrimSuffix(destination, filepath.Ext(destination)) + filepath.Ext(src)
	return moveFile(src, destination)
	// 历史资产：return moveFile(src, engine.AvatarPath(a.Config.ArchiveDir, sub))。
}

// migrateAvatarExtensions 只处理 downloads/archive 的旧头像，不下载也不复制文件。
func (a *App) migrateAvatarExtensions() {
	for _, root := range []string{a.Config.DownloadDir, a.Config.ArchiveDir} {
		if root == "" {
			continue
		}
		files, err := filepath.Glob(filepath.Join(root, "avatars", "instagram", "*.img"))
		if err != nil {
			slog.Warn("头像后缀迁移失败", "stage", "avatar-migration", "error", err)
			continue
		}
		for _, path := range files {
			destination, err := engine.NormalizeAvatar(path)
			if err != nil {
				slog.Warn("头像后缀迁移失败", "stage", "avatar-migration", "file", filepath.Base(path), "error", err)
				continue
			}
			if destination != path {
				slog.Info("头像后缀已更新", "stage", "avatar-migration", "from", filepath.Base(path), "to", filepath.Base(destination))
			}
		}
	}
}
