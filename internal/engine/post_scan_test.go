package engine

import (
	"context"
	"media-harbor/internal/domain"
	"testing"
)

// 已知单视频 URL 的扫描不启动 SDK；完整信息由核对后的 Prepare 唯一获取。
func TestSingleVideoScanUsesIdentityWithoutSDK(t *testing.T) {
	c := NewCLI(t.TempDir(), nil)
	t.Setenv("PATH", t.TempDir())
	for _, address := range []string{"https://www.youtube.com/watch?v=abc123", "https://youtu.be/abc123?t=1", "https://www.youtube.com/shorts/abc123"} {
		var items []domain.Content
		_, err := c.Scan(context.Background(), domain.Subscription{Platform: "youtube", URL: address}, 10, func(string) {}, func(batch domain.ScanResult) error { items = append(items, batch.Items...); return nil })
		if err != nil || len(items) != 1 || items[0].RemoteID != "abc123" {
			t.Fatalf("items=%+v err=%v", items, err)
		}
	}
}
