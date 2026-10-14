package service

import (
	"context"
	"media-harbor/internal/domain"
)

// registerPost 与初始化共用应用锁，已取消引擎的晚到回调不能重建已清空的详情。
// 不在平台请求期间持锁，只保护数据库中的关联创建。
func (a *App) registerPost(ctx context.Context, sid int64, platform string, item domain.Content, options domain.YouTubeOptions) (domain.PostTask, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.PostTask{}, err
	}
	return a.Repo.RegisterPost(sid, platform, item, options)
}

// reconcileFullScanLocked 为本轮每个资源重新建下载执行，不以旧完成状态跳过。
// 仅文件索引继续复用；重复交付由仓储按扫描来源幂等处理。
func (a *App) reconcileFullScanLocked(sid int64, item domain.Content) (added, retried, duplicate int, err error) {
	contents, err := a.Repo.Contents()
	if err != nil {
		return
	}
	exists := false
	for _, content := range contents {
		if content.SubscriptionID == sid && content.RemoteID == item.RemoteID {
			exists = true
			break
		}
	}
	queued, err := a.Repo.QueueFullContent(sid, item)
	if err != nil {
		return
	}
	if !queued {
		duplicate = 1
	} else if exists {
		retried = 1
	} else {
		added = 1
	}
	return
}
