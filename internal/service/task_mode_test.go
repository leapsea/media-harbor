package service

import (
	"context"
	"media-harbor/internal/domain"
	"os"
	"strings"
	"testing"
	"time"
)

// TestProfileWaitsForManualStart 主页保存后连定时也不启动；首次显式扫描后才启用周期。
func TestProfileWaitsForManualStart(t *testing.T) {
	for _, kind := range []string{"full", "scan", "catchup"} {
		t.Run(kind, func(t *testing.T) {
			app := testApp(t, fakeEngine{})
			sub := domain.Subscription{Platform: "youtube", URL: "https://www.youtube.com/@author", DisplayName: "author", Schedule: "@every 6h", Enabled: true}
			if err := app.SaveSubscription(&sub); err != nil {
				t.Fatal(err)
			}
			if !sub.NextRun.IsZero() {
				t.Fatal("未初始化主页不应安排下次执行")
			}
			if err := app.tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			tasks, _ := app.Repo.Tasks()
			if len(tasks) != 0 {
				t.Fatal("创建主页产生了执行动作", tasks)
			}
			if _, err := app.QueueScan(sub.ID, kind); err != nil {
				t.Fatal(err)
			}
			if err := app.tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			app.wg.Wait()
			latest, _ := app.Repo.Subscription(sub.ID)
			if latest.LastScan.IsZero() || latest.NextRun.IsZero() {
				t.Fatal("首次启动后未启用周期", latest)
			}
			contents, _ := app.Repo.Contents()
			if len(contents) != 1 {
				t.Fatal("初始化扫描未发现内容", contents)
			}
		})
	}
}

// TestDetailRunsOnlyOnce 详情创建立即排队，恶意或旧客户端提交周期也不会循环。
func TestDetailRunsOnlyOnce(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := domain.Subscription{Platform: "youtube", URL: "https://www.youtube.com/watch?v=one&list=playlist", DisplayName: "author", Schedule: "@every 6h", Enabled: true}
	if err := app.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	if sub.Mode != "content" || sub.Enabled || sub.Schedule != "" || !sub.NextRun.IsZero() {
		t.Fatal(sub)
	}
	tasks, _ := app.Repo.Tasks()
	if len(tasks) != 1 || tasks[0].Kind != "resolve" {
		t.Fatal(tasks)
	}
	for i := 0; i < 2; i++ {
		if err := app.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		app.wg.Wait()
	}
	// 即使旧配置人为设置已到期，调度器也不得重新排入扫描。
	latest, _ := app.Repo.Subscription(sub.ID)
	latest.Enabled = true
	latest.Schedule = "@every 6h"
	latest.NextRun = time.Now().Add(-time.Hour)
	app.Repo.SaveSubscription(&latest)
	if err := app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks, _ = app.Repo.Tasks()
	if len(tasks) != 2 {
		t.Fatal("详情产生周期任务", tasks)
	}
}

// TestFreshTaskLog 新库首次执行覆盖旧编号的日志，避免显示其他任务历史。
func TestFreshTaskLog(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "author", "youtube")
	task, err := app.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(app.LogPath(task), []byte("旧库日志"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.wg.Wait()
	raw, err := os.ReadFile(app.LogPath(task))
	if err != nil || strings.Contains(string(raw), "旧库日志") {
		t.Fatalf("%s %v", raw, err)
	}
}

// TestProfileScanAfterContent 同一博主的详情解析后，主页全量扫描可保存身份和入队内容。
func TestProfileScanAfterContent(t *testing.T) {
	app := testApp(t, fakeEngine{})
	detail := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/p/post-one/", DisplayName: "author", StableID: "platform-123"}
	if err := app.Repo.SaveSubscription(&detail); err != nil {
		t.Fatal(err)
	}
	profile := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/author/", DisplayName: "author"}
	if err := app.SaveSubscription(&profile); err != nil {
		t.Fatal(err)
	}
	scan, err := app.QueueScan(profile.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	scan.Status = "running"
	if err := app.Repo.SaveTask(&scan); err != nil {
		t.Fatal(err)
	}
	settings, _ := app.Repo.Settings()
	app.execute(context.Background(), scan, profile, settings)
	saved, err := app.Repo.Task(scan.ID)
	if err != nil || saved.Status != "completed" {
		t.Fatal(saved, err)
	}
	latest, _ := app.Repo.Subscription(profile.ID)
	items, _ := app.Repo.Contents()
	if latest.StableID != detail.StableID || len(items) != 1 || items[0].SubscriptionID != profile.ID {
		t.Fatal(latest, items)
	}
}

// TestUnstartedTaskLog 未启动与取消的任务不能展示旧库同编号的登录错误。
func TestUnstartedTaskLog(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "author", "instagram")
	task, err := app.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(app.LogPath(task), []byte("HTTP redirect to login page"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, cancelled := range []bool{false, true} {
		if cancelled {
			if err = app.CancelTask(task.ID); err != nil {
				t.Fatal(err)
			}
		}
		raw, err := app.TaskLog(task.ID)
		if err != nil || strings.Contains(raw, "redirect") {
			t.Fatalf("single=%s err=%v", raw, err)
		}
		raw, err = app.GroupLog(sub.ID)
		if err != nil || strings.Contains(raw, "redirect") {
			t.Fatalf("group=%s err=%v", raw, err)
		}
		if cancelled && !strings.Contains(raw, "未开始执行，已取消") {
			t.Fatal(raw)
		}
	}
}

// TestTaskLogIdentity 创建时间不同的同编号任务必须隔离日志，重试身份保持不变。
func TestTaskLogIdentity(t *testing.T) {
	app := testApp(t, fakeEngine{})
	old := domain.Task{ID: 63, CreatedAt: time.Date(2026, 10, 11, 15, 57, 48, 123, time.UTC), Attempts: 1, Status: "failed"}
	current := old
	current.CreatedAt = current.CreatedAt.Add(24 * time.Hour)
	current.Status = "running"
	if app.LogPath(old) == app.LogPath(current) {
		t.Fatal("同编号任务日志路径重叠")
	}
	if err := os.WriteFile(app.LogPath(old), []byte(taskLogLine(old.CreatedAt, "旧登录错误")), 0644); err != nil {
		t.Fatal(err)
	}
	raw, err := app.taskLog(current)
	if err != nil || strings.Contains(raw, "旧登录错误") {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	retry := current
	retry.Attempts++
	if app.LogPath(retry) != app.LogPath(current) {
		t.Fatal("重试更换了日志身份")
	}
	line := taskLogLine(current.CreatedAt, "事件")
	if !strings.Contains(line, current.CreatedAt.Local().Format("2006-01-02 15:04:05")) {
		t.Fatal(line)
	}
}
