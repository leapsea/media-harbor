package service

import (
	"context"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
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

// TestFreshTaskLog 新任务只读取新的订阅日志文件，不读取旧式任务日志。
func TestFreshTaskLog(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "author", "youtube")
	task, err := app.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(app.Config.LogDir, taskKey(task)+".log")
	if err = os.WriteFile(legacyPath, []byte("旧式任务日志"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.wg.Wait()
	raw, err := app.GroupLog(sub.ID)
	if err != nil || strings.Contains(raw, "旧式任务日志") || !strings.Contains(raw, "开始解析订阅内容") {
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

// TestUnstartedTaskLog 未启动与取消的任务没有运行日志，旧式文件不会被读取。
func TestUnstartedTaskLog(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "author", "instagram")
	task, err := app.QueueScan(sub.ID, "full")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(app.Config.LogDir, taskKey(task)+".log"), []byte("HTTP redirect to login page"), 0644); err != nil {
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
		if raw != "" {
			t.Fatal(raw)
		}
	}
}

// TestTaskLogIdentity 验证同一订阅共用单个日期日志，任务接口仍可按任务标识筛选。
func TestTaskLogIdentity(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "author name", "youtube")
	for _, remoteID := range []string{"post-a", "post-b"} {
		if err := app.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: remoteID}); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := app.Repo.Tasks()
	if err != nil || len(tasks) != 2 {
		t.Fatal(tasks, err)
	}
	first, second := tasks[0], tasks[1]
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
	if app.LogPath(sub, base) != app.LogPath(sub, base.Add(time.Hour)) {
		t.Fatal("同一天同一订阅应复用日志路径")
	}
	if app.LogPath(sub, base) == app.LogPath(sub, base.Add(24*time.Hour)) {
		t.Fatal("不同日期应使用不同日志路径")
	}
	for _, entry := range []struct {
		task    domain.Task
		message string
	}{{first, "请求资源 A 成功"}, {second, "请求资源 B 失败"}} {
		if err := app.writeSubscriptionLog(sub, entry.task, time.Now(), entry.message); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []struct {
		task      domain.Task
		want      string
		otherWant string
	}{{first, "请求资源 A 成功", "请求资源 B 失败"}, {second, "请求资源 B 失败", "请求资源 A 成功"}} {
		raw, err := app.TaskLog(entry.task.ID)
		if err != nil || !strings.Contains(raw, entry.want) {
			t.Fatalf("task=%d raw=%s err=%v", entry.task.ID, raw, err)
		}
		if strings.Contains(raw, entry.otherWant) {
			t.Fatalf("task log contains another task: %s", raw)
		}
	}
	path := app.LogPath(sub, base)
	if filepath.Base(path) != "author_name_20261013.log" {
		t.Fatal(path)
	}
	entries, err := os.ReadDir(app.Config.LogDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("per-post log files were created: entries=%v err=%v", entries, err)
	}
}
