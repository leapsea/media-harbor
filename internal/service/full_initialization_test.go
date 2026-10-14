package service

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

// 覆盖引擎拒绝旧路径，输出不同文件名以验证标题变更仍覆盖原位置。
type overwriteEngine struct{ fakeEngine }

func (overwriteEngine) Download(_ context.Context, _ domain.Subscription, item domain.Content, _ domain.YouTubeOptions, dir string, _ func(string)) error {
	if len(item.Paths) > 0 {
		return errors.New("全量下载错误复用了旧文件")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "new-title.mp4"), []byte("fresh-media"), 0644)
}

// 调试范围重新开始，旧路径保留到新下载完成；下载卷和归档卷均覆盖原文件。
func TestFullInitializationClearsDetailsAndOverwrites(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "downloads", true: "archive"}[archive], func(t *testing.T) {
			a := testApp(t, overwriteEngine{})
			sub := addSub(t, a, "reset", "youtube")
			if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
				t.Fatal(err)
			}
			tasks, _ := a.Repo.SubscriptionTasks(sub.ID)
			oldTask := tasks[0]
			item, _ := a.Repo.Content(oldTask.ContentID)
			root := a.Config.DownloadDir
			if archive {
				root = a.Config.ArchiveDir
			}
			path := filepath.Join(root, "original-title.mp4")
			if err := os.WriteFile(path, []byte("old-media"), 0644); err != nil {
				t.Fatal(err)
			}
			item.Paths, item.Status = []string{path}, "completed"
			if err := a.Repo.SaveContent(&item); err != nil {
				t.Fatal(err)
			}
			oldTask.Status = "completed"
			if err := a.Repo.SaveTask(&oldTask); err != nil {
				t.Fatal(err)
			}
			settings, _ := a.Repo.Settings()
			settings.Debug.TestItemLimit = 1
			if err := a.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			scan, err := a.QueueScan(sub.ID, "full")
			if err != nil {
				t.Fatal(err)
			}
			detail, err := a.TaskDetails(context.Background(), sub.ID)
			if err != nil || len(detail.Items) != 0 {
				t.Fatal(detail, err)
			}
			if data, _ := os.ReadFile(path); string(data) != "old-media" {
				t.Fatal("重新下载前移除了旧文件")
			}
			// 一条旧完成任务足以占满调试范围，验证新下载不会因此被卡住。
			for range 6 {
				if err = a.tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				a.wg.Wait()
			}
			current, _ := a.Repo.Task(scan.ID)
			if current.Status != "completed" {
				t.Fatal(current)
			}
			if data, _ := os.ReadFile(path); string(data) != "fresh-media" {
				t.Fatal("没有覆盖原位置", string(data))
			}
			updated, _ := a.Repo.Content(item.ID)
			if len(updated.Paths) != 1 || updated.Paths[0] != path || updated.Status != "completed" {
				t.Fatal(updated)
			}
			detail, err = a.TaskDetails(context.Background(), sub.ID)
			if err != nil || len(detail.Items) != 1 || detail.Items[0].Status != "completed" {
				t.Fatal(detail, err)
			}
		})
	}
}

// 初始化不等待慢引擎停止，但平台新批次必须等其实际退出才认领。
func TestFullInitializationStopsOldRunWithoutLateWrites(t *testing.T) {
	e := delayedStopEngine{started: make(chan struct{}), stopping: make(chan struct{}), release: make(chan struct{})}
	a := testApp(t, e)
	sub := addSub(t, a, "stop-old", "youtube")
	old, err := a.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); close(e.release); a.wg.Wait() }()
	if err = a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	<-e.started
	newTask, err := a.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	<-e.stopping
	prior, _ := a.Repo.Task(old.ID)
	if prior.Status != "cancelled" {
		t.Fatal(prior)
	}
	if err = a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	queued, _ := a.Repo.Task(newTask.ID)
	if queued.Status != "queued" {
		t.Fatal("旧进程退出前启动了新批次", queued)
	}
	late, stop := context.WithCancel(context.Background())
	stop()
	if _, err = a.registerPost(late, sub.ID, "youtube", domain.Content{RemoteID: "late"}, domain.YouTubeOptions{Video: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	detail, err := a.TaskDetails(context.Background(), sub.ID)
	if err != nil || len(detail.Items) != 0 {
		t.Fatal("旧回调恢复了详情", detail, err)
	}
}
