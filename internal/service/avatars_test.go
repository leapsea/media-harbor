package service

import (
	"context"
	"fmt"
	"media-harbor/internal/domain"
	"media-harbor/internal/engine"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// avatarEngine 记录所属下载的作者身份，可分别验证成功与失败路径。
type avatarEngine struct {
	fakeEngine
	author domain.Subscription
	fail   bool
}

func (e *avatarEngine) UpdateAvatar(_ context.Context, author domain.Subscription, path string, _ func(string)) error {
	e.author = author
	if e.fail {
		return fmt.Errorf("头像测试错误")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("avatar-fixture"), 0644)
}

// TestAvatarFailureDoesNotFailDownload 扫描不取头像，下载阶段头像失败不撤销媒体成果。
func TestAvatarFailureDoesNotFailDownload(t *testing.T) {
	eng := &avatarEngine{fail: true}
	app := testApp(t, eng)
	sub := addSub(t, app, "author", "instagram")
	settings, _ := app.Repo.Settings()
	settings.Debug.TestItemLimit = 10
	if err := app.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	task, err := app.QueueScan(sub.ID, "scan")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.wg.Wait()
	saved, _ := app.Repo.Task(task.ID)
	if saved.Status != "completed" || eng.author.StableID != "" {
		t.Fatal(saved, eng.author)
	}
	if err = app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.wg.Wait()
	contents, _ := app.Repo.Contents()
	if len(contents) != 1 || contents[0].Status != "completed" || eng.author.StableID != "platform-123" || eng.author.CurrentName != "renamed" {
		t.Fatal(contents, eng.author)
	}
	tasks, _ := app.Repo.Tasks()
	for _, task := range tasks {
		if task.ContentID == 0 {
			continue
		}
		log, err := app.TaskLog(task.ID)
		if err != nil || !strings.Contains(log, "头像测试错误") {
			t.Fatal(log, err)
		}
	}
}

// TestAvatarFollowsDownloadArchive 覆盖下载关联、归档卷恢复、归档后读取与旧 data 不兼容。
func TestAvatarFollowsDownloadArchive(t *testing.T) {
	eng := &avatarEngine{}
	app := testApp(t, eng)
	sub := addSub(t, app, "author", "instagram")
	if _, err := app.QueueScan(sub.ID, "full"); err != nil {
		t.Fatal(err)
	}
	if err := app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.wg.Wait()
	sub, _ = app.Repo.Subscription(sub.ID)
	old := engine.AvatarPath(app.Config.DataDir, sub)
	if err := os.MkdirAll(filepath.Dir(old), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old-avatar"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AvatarFile(sub.ID); !os.IsNotExist(err) {
		t.Fatalf("读取了旧 data 头像：%v", err)
	}
	// 归档开关打开但卷未挂载，下载及头像暂留 downloads。
	sub.Archive = true
	if err := app.Repo.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	if err := app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.wg.Wait()
	download := engine.AvatarPath(app.Config.DownloadDir, sub)
	path, err := app.AvatarFile(sub.ID)
	if err != nil || path != download {
		t.Fatal(path, err)
	}
	if err := app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(download); err != nil {
		t.Fatal("卷不可用时移动了头像", err)
	}
	// 恢复归档能力后，通过正常调度器移动媒体与共享头像。
	app.directories[2].Mounted = "mounted"
	app.directories[2].Writable = true
	if err := app.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	archived := engine.AvatarPath(app.Config.ArchiveDir, sub)
	path, err = app.AvatarFile(sub.ID)
	if err != nil || path != archived {
		t.Fatal(path, err)
	}
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Fatal("下载目录残留头像副本", err)
	}
	items, _ := app.Repo.Contents()
	if !items[0].Archived {
		t.Fatal(items)
	}
	// 后续下载刷新归档中的共享头像，不重新创建 downloads 副本。
	app.updateDownloadAvatar(context.Background(), sub, func(string) {})
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Fatal("归档后刷新产生副本", err)
	}
	body, err := os.ReadFile(old)
	if err != nil || string(body) != "old-avatar" {
		t.Fatal("修改了旧头像", err)
	}
}
