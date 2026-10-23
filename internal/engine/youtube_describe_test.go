package engine

import (
	"media-harbor/internal/domain"
	"testing"
	"time"
)

// 详情地址的扫描项没有日期与标题，解析结果在入队前补全。
func TestDescribeYouTubeFillsPublicationFields(t *testing.T) {
	item := domain.Content{RemoteID: "v", URL: "https://www.youtube.com/watch?v=v"}
	got := describeYouTube(item, []byte(`{"upload_date":"20251217","title":"标题","channel_id":"UC1","availability":"public"}`))
	if want := time.Date(2025, 12, 17, 0, 0, 0, 0, time.UTC); !got.Published.Equal(want) || got.Title != "标题" || got.ChannelID != "UC1" || got.Availability != "public" {
		t.Fatalf("未补全内容: %+v", got)
	}
	got = describeYouTube(item, []byte(`{"timestamp":1765987213}`))
	if got.Published.IsZero() {
		t.Fatal("缺少 upload_date 时应使用 timestamp")
	}
	if got = describeYouTube(domain.Content{Title: "旧", Published: item.Published}, []byte(`{}`)); got.Title != "旧" {
		t.Fatalf("缺失字段不应清空原值: %+v", got)
	}
}
