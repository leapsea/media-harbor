package service

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// publicationEngine 模拟成功解析日期后媒体下载失败，不生成媒体产物。
type publicationEngine struct{ fakeEngine }

func (publicationEngine) Download(_ context.Context, _ domain.Subscription, _ domain.Content, _ domain.YouTubeOptions, dir string, _ func(string)) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ".metadata.json"), []byte(`{"upload_date":"20260531"}`), 0644); err != nil {
		return err
	}
	return errors.New("media transfer failed")
}

// TestPublicationSavedBeforeArtifacts 日期入库不依赖媒体成功落盘，也覆盖扫描的旧日期。
func TestPublicationSavedBeforeArtifacts(t *testing.T) {
	for _, scanned := range []time.Time{{}, time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)} {
		a := testApp(t, publicationEngine{})
		sub := addSub(t, a, "publication", "youtube")
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "id", Published: scanned}); err != nil {
			t.Fatal(err)
		}
		if err := a.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		a.wg.Wait()
		items, err := a.Repo.Contents()
		if err != nil || len(items) != 1 {
			t.Fatal(items, err)
		}
		item := items[0]
		if item.Published.Format("20060102") != "20260531" || item.Status != "failed" {
			t.Fatal(item)
		}
		// 补全扫描无日期时，保留真实单视频元数据，不能重新变成零值。
		if err := a.retryContentLocked(item, domain.Content{RemoteID: "id"}, domain.Task{}); err != nil {
			t.Fatal(err)
		}
		saved, err := a.Repo.Content(item.ID)
		if err != nil || !saved.Published.Equal(item.Published) {
			t.Fatal(saved, err)
		}
	}
}

// TestPublicationLogLabel 未取得日期与有效日期在日志中分别展示。
func TestPublicationLogLabel(t *testing.T) {
	if got := publicationLabel(time.Time{}); got != "待解析" {
		t.Fatal(got)
	}
	if got := publicationLabel(time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)); got != "2026-05-31" {
		t.Fatal(got)
	}
}
