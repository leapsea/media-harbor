package service

import (
	"context"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"media-harbor/internal/storage"
	"testing"
	"time"
)

// TestRestartResumesTasks 覆盖正常停止与进程中断，重启沿用任务和工作目录自动执行。
func TestRestartResumesTasks(t *testing.T) {
	for _, graceful := range []bool{true, false} {
		name := "abrupt"
		if graceful {
			name = "graceful"
		}
		t.Run(name, func(t *testing.T) {
			a := testApp(t, fakeEngine{block: true})
			sub := addSub(t, a, "restart", "youtube")
			// 多阶段视频的附属项在重新认领时仍待执行，不能残留上次停止说明。
			sub.YouTube = &domain.YouTubeOptions{Video: true, Comments: true, Subtitles: true}
			if err := a.Repo.SaveSubscription(&sub); err != nil {
				t.Fatal(err)
			}
			scan, err := a.QueueScan(sub.ID, "full")
			if err != nil {
				t.Fatal(err)
			}
			if err = a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "resume"}); err != nil {
				t.Fatal(err)
			}
			tasks, _ := a.Repo.Tasks()
			var download domain.Task
			for _, task := range tasks {
				if task.ContentID != 0 {
					download = task
				}
			}
			work := a.WorkPath(download)
			if graceful {
				ctx, cancel := context.WithCancel(context.Background())
				if err = a.tick(ctx); err != nil {
					cancel()
					t.Fatal(err)
				}
				cancel()
				a.wg.Wait()
			} else {
				for _, task := range []domain.Task{scan, download} {
					task.Status = "running"
					task.Attempts = 1
					task.StartedAt = time.Now()
					if err = a.Repo.SaveTask(&task); err != nil {
						t.Fatal(err)
					}
				}
				item, _ := a.Repo.Content(download.ContentID)
				item.Status = "running"
				item.Paths = []string{"existing.audio.m4a"}
				if err = a.Repo.SaveContent(&item); err != nil {
					t.Fatal(err)
				}
			}
			db := a.Repo.(*storage.Store)
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := storage.Open(a.Config.DataDir+"/media.db", config.Defaults())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			resumed := New(a.Config, reopened, fakeEngine{block: true})
			if resumed.WorkPath(download) != work {
				t.Fatal("重启改变工作目录")
			}
			if !graceful {
				item, _ := reopened.Content(download.ContentID)
				if item.Status != "queued" || len(item.Paths) != 1 {
					t.Fatal(item)
				}
			}
			startEventScheduler(t, resumed)
			deadline := time.Now().Add(3 * time.Second)
			for {
				first, _ := reopened.Task(scan.ID)
				second, _ := reopened.Task(download.ID)
				if first.Status == "running" && second.Status == "running" {
					if first.Attempts != 2 || second.Attempts != 2 || first.Error != "" || second.Error != "" {
						t.Fatal(first, second)
					}
					// 同时核对详情 API 投影，单查执行账本看不到附属项携带的旧提示。
					details, err := resumed.TaskDetails(context.Background(), sub.ID)
					if err != nil || len(details.Items) != 1 || details.Items[0].Error != "" {
						t.Fatalf("恢复后的详情仍有停止提示：%+v，%v", details, err)
					}
					for _, part := range details.Items[0].Parts {
						if part.Error != "" {
							t.Fatalf("恢复后的附属项仍有停止提示：%+v", part)
						}
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("重启未自动继续", first, second)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
