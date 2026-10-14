package service

import (
	"context"
	"media-harbor/internal/domain"
	"testing"
)

// TestInstagramCredentialPauseKeepsQueue 缺少Cookie仅暂停Instagram，YouTube仍运行。
func TestInstagramCredentialPauseKeepsQueue(t *testing.T) {
	a := testApp(t, fakeEngine{block: true})
	if err := a.Credentials.Delete("instagram"); err != nil {
		t.Fatal(err)
	}
	ig := addSub(t, a, "ig", "instagram")
	yt := addSub(t, a, "yt", "youtube")
	for _, sub := range []domain.Subscription{ig, yt} {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
			t.Fatal(err)
		}
	}
	// 引擎阻塞直到取消，能直接观察哪个平台实际获准启动。
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel() // 等待进程退出后的领域回写，再核对任务没有被用户取消。
		a.wg.Wait()
	}()
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	// 本地持久状态与实际运行计数都不能把Instagram认为已启动。
	// 同一数据库记录继续保留，Cookie编辑后能恢复相同任务ID。
	tasks, _ := a.Repo.Tasks()
	for _, task := range tasks {
		if task.SubscriptionID == ig.ID && task.Status != "queued" || task.SubscriptionID == yt.ID && task.Status != "running" {
			t.Fatal(task)
		}
	}
	// API汇总明确展示平台停用，但下载计数不改变。
	groups, _ := a.TaskGroups()
	for _, g := range groups {
		if g.PlatformDisabled != (g.SubscriptionID == ig.ID) {
			t.Fatal(g)
		}
	}
	// 保存新Cookie后原ID被认领，不清空或复制旧任务。
	if err := a.Credentials.Save("instagram", "# Netscape HTTP Cookie File\n.instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\tnew-offline\n"); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, _ = a.Repo.Tasks()
	if len(tasks) != 2 {
		t.Fatal(tasks)
	}
	for _, task := range tasks {
		if task.Status != "running" {
			t.Fatal(task)
		}
	}
}

// TestCredentialInvalidationStopsActive Instagram停用后释放进程，任务回到队列等待编辑。
func TestCredentialInvalidationStopsActive(t *testing.T) {
	a := testApp(t, fakeEngine{block: true})
	sub := addSub(t, a, "ig", "instagram")
	if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "one"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	// 模拟当前版本被平台明确拒绝，而不是把一般下载错误当作失效。
	state, _ := a.Credentials.Status("instagram")
	_, _ = a.Credentials.Disable("instagram", state.Revision, "Instagram 登录会话失效")
	// 下一轮调度只停止本平台，在完成回写时保留恢复任务。
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	tasks, _ := a.Repo.Tasks()
	if len(tasks) != 1 || tasks[0].Status != "queued" || tasks[0].Error == "" {
		t.Fatal(tasks)
	}
}
