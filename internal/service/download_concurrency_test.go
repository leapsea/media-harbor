package service

import (
	"context"
	"fmt"
	"media-harbor/internal/domain"
	"testing"
)

// TestDownloadsUsePlatformSlots 同一配置的下载可填满平台额度，统计对应实际运行记录。
func TestDownloadsUsePlatformSlots(t *testing.T) {
	for _, tc := range []struct {
		platform string
		limit    int
	}{{"youtube", 2}, {"instagram", 10}} {
		t.Run(tc.platform, func(t *testing.T) {
			a := testApp(t, fakeEngine{block: true})
			settings, _ := a.Repo.Settings()
			settings.YouTubeConcurrent = 2
			settings.InstagramConcurrent = 10
			if err := a.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			sub := addSub(t, a, "concurrent", tc.platform)
			for i := 0; i < tc.limit+3; i++ {
				if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: fmt.Sprint(i), URL: "https://example.com/media"}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); a.wg.Wait() }()
			if err := a.tick(ctx); err != nil {
				t.Fatal(err)
			}
			tasks, _ := a.Repo.Tasks()
			running, queued := 0, 0
			for _, task := range tasks {
				if task.Status == "running" {
					running++
				}
				if task.Status == "queued" {
					queued++
				}
			}
			if running != tc.limit || queued != 3 {
				t.Fatalf("running=%d queued=%d limit=%d", running, queued, tc.limit)
			}
			groups, err := a.TaskGroups()
			if err != nil || len(groups) != 1 || groups[0].Counts["running"] != tc.limit || groups[0].Counts["queued"] != 3 {
				t.Fatal(groups, err)
			}
			// 第二次调度不能突破上限，即使同平台还有其他配置等待。
			other := addSub(t, a, "other", tc.platform)
			if err := a.Repo.AddContentAndTask(other.ID, domain.Content{RemoteID: "other"}); err != nil {
				t.Fatal(err)
			}
			if err := a.tick(ctx); err != nil {
				t.Fatal(err)
			}
			tasks, _ = a.Repo.Tasks()
			running = 0
			for _, task := range tasks {
				if task.Status == "running" {
					running++
				}
			}
			if running != tc.limit {
				t.Fatalf("平台额度被突破：%d", running)
			}
		})
	}
}
