package service

import (
	"media-harbor/internal/domain"
	"strings"
)

// ClearDownloadHistory 与下载状态提交使用同一应用锁，明确本次清空的记录范围。
// 历史仓储只处理展示，扫描和补全继续读取完整下载索引。
func (a *App) ClearDownloadHistory() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Repo.ClearDownloadHistory()
}

// ContentHistory 按实际文件读取产物类型与评论数量，不重写内容快照。
// 新下载的标签已持久化；历史文件缺失时不能凭配置声称评论已保存。
func (a *App) ContentHistory() ([]domain.Content, error) {
	items, err := a.Repo.DownloadHistory()
	if err != nil {
		return nil, err
	}
	subs, err := a.Repo.Subscriptions()
	if err != nil {
		return nil, err
	}
	enrichHistory(items, subs)
	return items, nil
}

// ContentHistoryPage 只补充当前页所需的文件元数据，避免对所有历史产物做文件读取。
func (a *App) ContentHistoryPage(offset, limit int) ([]domain.Content, int, error) {
	items, err := a.Repo.DownloadHistoryPage(offset, limit)
	if err != nil {
		return nil, 0, err
	}
	count, err := a.Repo.DownloadHistoryCount()
	if err != nil {
		return nil, 0, err
	}
	subs, err := a.Repo.Subscriptions()
	if err != nil {
		return nil, 0, err
	}
	enrichHistory(items, subs)
	return items, count, nil
}

func enrichHistory(items []domain.Content, subs []domain.Subscription) {
	platforms := map[int64]string{}
	for _, s := range subs {
		platforms[s.ID] = s.Platform
	}
	for i := range items {
		// 文件与标签一起返回，避免界面把内容汇总标签分配到错误文件。
		items[i].Files = []domain.ContentFile{}
		for _, path := range items[i].Paths {
			file := domain.ContentFile{Path: path}
			if strings.HasSuffix(path, ".info.json") {
				file = infoFileDetails(path)
			} else {
				file.Tags = fileTags(path, platforms[items[i].SubscriptionID])
			}
			tags := file.Tags
			items[i].Files = append(items[i].Files, file)
			for _, tag := range tags {
				items[i].Tags = appendUnique(items[i].Tags, tag)
			}
		}
	}
}
