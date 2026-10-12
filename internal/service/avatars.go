package service

import (
	"fmt"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"os"
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
		if info, err := os.Stat(engine.AvatarPath(a.Config.DataDir, sub)); err == nil {
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
	path := engine.AvatarPath(a.Config.DataDir, sub)
	if path == "" {
		return "", os.ErrNotExist
	}
	_, err = os.Stat(path)
	return path, err
}
