package service

import (
	"context"
	"fmt"
	"media-harbor/internal/domain"
	"strings"
	"testing"
)

// avatarEngine 保持媒体扫描成功，但让头像失败以验证两者的生命周期独立。
type avatarEngine struct {
	fakeEngine
	author domain.Subscription
}

func (e *avatarEngine) UpdateAvatar(_ context.Context, author domain.Subscription, _ func(string)) error {
	e.author = author
	return fmt.Errorf("头像测试错误")
}

// TestAvatarFailureDoesNotFailScan 头像使用扫描后的作者身份，失败不撤销媒体或扫描状态。
func TestAvatarFailureDoesNotFailScan(t *testing.T) {
	eng := &avatarEngine{}
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
	if saved.Status != "completed" || eng.author.StableID != "platform-123" || eng.author.CurrentName != "renamed" {
		t.Fatal(saved, eng.author)
	}
	log, err := app.TaskLog(task.ID)
	if err != nil || !strings.Contains(log, "头像测试错误") {
		t.Fatal(log, err)
	}
}
