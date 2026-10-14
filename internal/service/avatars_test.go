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

// ResolveAuthor 提供创建时的稳定身份，不启动媒体扫描。
func (e *avatarEngine) ResolveAuthor(_ context.Context, _ domain.Subscription) (domain.ScanResult, error) {
	return domain.ScanResult{StableID: "platform-123", CurrentName: "renamed"}, nil
}

// TestAvatarAtCreation 创建后即可读取头像，主页尚未生成媒体任务。
func TestAvatarAtCreation(t *testing.T) {
	eng := &avatarEngine{}
	app := testApp(t, eng)
	sub := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/author/", DisplayName: "custom"}
	if err := app.CreateSubscription(context.Background(), &sub); err != nil {
		t.Fatal(err)
	}
	if sub.StableID != "platform-123" || sub.CurrentName != "renamed" || sub.DisplayName != "custom" {
		t.Fatal(sub)
	}
	if path, err := app.AvatarFile(sub.ID); err != nil || path != engine.AvatarPath(app.Config.DownloadDir, sub) {
		t.Fatal(path, err)
	}
	tasks, _ := app.Repo.Tasks()
	if len(tasks) != 0 {
		t.Fatal("主页创建不应启动媒体任务", tasks)
	}
	views, err := app.SubscriptionViews()
	if err != nil || len(views) != 1 || views[0].AvatarURL == "" {
		t.Fatal(views, err)
	}
}

// TestAvatarFailureDoesNotFailCreation 头像失败写入创建日志，不撤销配置。
func TestAvatarFailureDoesNotFailCreation(t *testing.T) {
	eng := &avatarEngine{fail: true}
	app := testApp(t, eng)
	sub := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/author/", DisplayName: "author"}
	if err := app.CreateSubscription(context.Background(), &sub); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Repo.Subscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	log, err := app.readSubscriptionLogs(sub)
	if err != nil || !strings.Contains(log, "头像测试错误") {
		t.Fatal(log, err)
	}
}

// TestAvatarFollowsDownloadArchive 覆盖下载关联、归档卷恢复、归档后读取与旧 data 不兼容。
func TestAvatarFollowsDownloadArchive(t *testing.T) {
	eng := &avatarEngine{}
	app := testApp(t, eng)
	sub := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/author/", DisplayName: "author"}
	if err := app.CreateSubscription(context.Background(), &sub); err != nil {
		t.Fatal(err)
	}
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
	// 下载目录的头像来自创建，旧 data 文件不参与读取。
	if path, err := app.AvatarFile(sub.ID); err != nil || path == old {
		t.Fatal("读取了旧 data 头像", path, err)
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
	app.updateCreationAvatar(context.Background(), sub, func(string) {})
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Fatal("归档后刷新产生副本", err)
	}
	body, err := os.ReadFile(old)
	if err != nil || string(body) != "old-avatar" {
		t.Fatal("修改了旧头像", err)
	}
}

// PNG归档必须仍是PNG，不能因为目标目录尚无头像而使用默认jpg后缀。
func TestAvatarArchivePreservesActualExtension(t *testing.T) {
	app := testApp(t, fakeEngine{})
	sub := addSub(t, app, "png-avatar", "instagram")
	sub.StableID = "png-author"
	if err := app.Repo.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSuffix(engine.AvatarPath(app.Config.DownloadDir, sub), ".jpg") + ".png"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	body := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32))
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	if err := app.archiveAvatar(sub); err != nil {
		t.Fatal(err)
	}
	archived, err := app.AvatarFile(sub.ID)
	if err != nil || filepath.Ext(archived) != ".png" || !strings.HasPrefix(archived, app.Config.ArchiveDir) {
		t.Fatal(archived, err)
	}
	// 归档移动只留一个文件，接口仍能从同一博主身份找到它。
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("下载目录残留副本", err)
	}
	data, err := os.ReadFile(archived)
	if err != nil || string(data) != string(body) {
		t.Fatal("归档改写图片内容", err)
	}
}
