package service

import (
	"context"
	"media-harbor/internal/domain"
	"reflect"
	"testing"
)

// preparationEngine 记录平台解析调用，仓储仍使用真实 SQLite。
type preparationEngine struct {
	fakeEngine
	prepared []string
}

func (e *preparationEngine) Scan(_ context.Context, _ domain.Subscription, _ int, _ func(string), emit func(domain.ScanResult) error) (domain.ScanResult, error) {
	// 完成、失败、新增及本轮重复分别覆盖不同入队决策。
	return domain.ScanResult{}, emit(domain.ScanResult{Items: []domain.Content{{RemoteID: "done"}, {RemoteID: "failed"}, {RemoteID: "new"}, {RemoteID: "new"}}})
}

func (e *preparationEngine) Prepare(_ context.Context, _ domain.Subscription, item domain.Content, _ domain.YouTubeOptions, _ func(string)) error {
	// 已有格式的重复和完成项不能被交给解析器重新请求。
	e.prepared = append(e.prepared, item.RemoteID)
	return nil
}

func TestProducerPreparesOnlyNeededDownloads(t *testing.T) {
	e := &preparationEngine{}
	a := testApp(t, e)
	sub := addSub(t, a, "preparation", "youtube")
	for _, status := range []string{"done", "failed"} {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: status}); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := a.Repo.Contents()
	// 保留失败索引，重试需要新的地址；完成项仍由本地索引决定跳过。
	for _, item := range items {
		item.Status = "failed"
		if item.RemoteID == "done" {
			item.Status = "completed"
		}
		if err := a.Repo.SaveContent(&item); err != nil {
			t.Fatal(err)
		}
	}
	// 测试同步交付，不需要真实下载文件或访问平台。
	_, err := a.streamScan(context.Background(), sub, 10, "full", func(string) {}, func(domain.ScanResult) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.prepared, []string{"failed", "new"}) {
		// 同一批次重复的新媒体也只能准备一次，不放大平台请求。
		t.Fatalf("重复解析：%v", e.prepared)
	}
}
