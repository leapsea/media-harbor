package service

import (
	"context"
	"media-harbor/internal/domain"
	"testing"
	"time"
)

// serviceSleepNow 让跨午夜的测试也覆盖当前窗口，不等待真实时钟跨分钟。
func serviceSleepNow() domain.SleepSettings {
	now := time.Now().UTC()
	return domain.SleepSettings{Enabled: true, Start: now.Add(-time.Hour).Format("15:04"), End: now.Add(time.Hour).Format("15:04"), Timezone: "UTC"}
}

// 调度、生产暂停与任务状态均按所属平台判断；另一平台完成下载并保持正常展示。
func TestPlatformSleepDoesNotBlockOtherPlatform(t *testing.T) {
	for _, paused := range []string{"instagram", "youtube"} {
		t.Run(paused, func(t *testing.T) {
			a := testApp(t, fakeEngine{})
			s, _ := a.Repo.Settings()
			awake := "youtube"
			if paused == "instagram" {
				s.Sleep.Instagram = serviceSleepNow()
			} else {
				s.Sleep.YouTube = serviceSleepNow()
				awake = "instagram"
			}
			if err := a.SaveSettings(s); err != nil {
				t.Fatal(err)
			}
			subs := map[string]domain.Subscription{}
			for _, platform := range []string{paused, awake} {
				sub := addSub(t, a, platform, platform)
				subs[platform] = sub
				if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: "media"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			a.wg.Wait()
			for _, platform := range []string{paused, awake} {
				tasks, _ := a.Repo.SubscriptionTasks(subs[platform].ID)
				want := "completed"
				if platform == paused {
					want = "queued"
				}
				if len(tasks) != 1 || tasks[0].Status != want {
					t.Fatal(platform, tasks)
				}
			}
			groups, err := a.TaskGroups()
			if err != nil {
				t.Fatal(err)
			}
			for _, group := range groups {
				if (group.SleepUntil != nil) != (group.SubscriptionID == subs[paused].ID) {
					t.Fatal("休眠状态扩散到其他平台", group)
				}
			}
			// 队列背压中的生产者同样不得因其他平台的休眠而等待。
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := a.waitProductionAwake(ctx, awake, "media", func(string) {}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestSleepQueuesDueAndManualTasks 周期到期不丢失，两个平台与手动下载都等待。
func TestSleepQueuesDueAndManualTasks(t *testing.T) {
	// 使用真实SQLite，检查持久任务ID与API汇总。
	a := testApp(t, fakeEngine{})
	s, _ := a.Repo.Settings()
	// 设置保存后当前进程上下文不能收到取消信号。
	s.Sleep.Instagram = serviceSleepNow()
	s.Sleep.YouTube = serviceSleepNow()
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	sub := addSub(t, a, "due", "instagram")
	// 模拟首次扫描之后的周期到期，避免未初始化任务自动执行。
	sub.Schedule = "@every 1h"
	sub.LastScan = time.Now().Add(-time.Hour)
	sub.NextRun = time.Now().Add(-time.Minute)
	if err := a.Repo.SaveSubscription(&sub); err != nil {
		t.Fatal(err)
	}
	other := addSub(t, a, "manual", "youtube")
	if err := a.Repo.AddContentAndTask(other.ID, domain.Content{RemoteID: "one", URL: "https://example.com/media"}); err != nil {
		t.Fatal(err)
	}
	// 多次调度也只能生成一条到期扫描，不重复补发。
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	if len(tasks) != 2 {
		t.Fatal("到期任务丢失或重复", tasks)
	}
	// 休眠不能修改排队任务的状态或重试次数。
	for _, task := range tasks {
		if task.Status != "queued" {
			t.Fatal(task)
		}
	}
	// 展示附加恢复时间，底层状态仍允许用户取消排队任务。
	groups, err := a.TaskGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.SleepUntil == nil {
			t.Fatal("没有显示休眠", g)
		}
	}
	// 关闭后原任务ID继续认领，未运行的周期不会重新创建另一条扫描。
	s.Sleep.Instagram.Enabled = false
	s.Sleep.YouTube.Enabled = false
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 引擎完成后再读取最终状态，排除异步提交竞态。
	a.wg.Wait()
	// 关闭休眠恢复原任务，不创建替代任务或复制下载记录。
	for _, old := range tasks {
		now, _ := a.Repo.Task(old.ID)
		if now.Status != "completed" {
			t.Fatal(now)
		}
	}
}

// TestSleepKeepsActiveDownload 进入休眠不会发送取消信号，也不认领后续媒体。
func TestSleepKeepsActiveDownload(t *testing.T) {
	a := testApp(t, fakeEngine{block: true})
	s, _ := a.Repo.Settings()
	s.InstagramConcurrent = 1
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	sub := addSub(t, a, "active", "instagram")
	for _, id := range []string{"one", "two"} {
		if err := a.Repo.AddContentAndTask(sub.ID, domain.Content{RemoteID: id}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); a.wg.Wait() }()
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	s.Sleep.Instagram = serviceSleepNow()
	if err := a.SaveSettings(s); err != nil {
		t.Fatal(err)
	}
	if err := a.tick(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, _ := a.Repo.Tasks()
	// Repository排序与执行顺序独立，只核对运行与等待数量。
	states := map[string]int{}
	for _, task := range tasks {
		states[task.Status]++
	}
	if states["running"] != 1 || states["queued"] != 1 || len(a.active) != 1 {
		t.Fatal(tasks)
	}
}

// TestSleepCreationReturnsWithoutRequests 创建配置不等待休眠结束或请求头像。
func TestSleepCreationReturnsWithoutRequests(t *testing.T) {
	eng := &avatarEngine{}
	a := testApp(t, eng)
	settings, _ := a.Repo.Settings()
	settings.Sleep.Instagram = serviceSleepNow()
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	sub := domain.Subscription{Platform: "instagram", URL: "https://www.instagram.com/new/"}
	if err := a.CreateSubscription(context.Background(), &sub); err != nil {
		t.Fatal(err)
	}
	// 稳定身份尚未解析，主页仅保存，详情则按既有规则生成等待任务。
	if sub.StableID != "" {
		t.Fatal("休眠期间仍解析身份", sub)
	}
	tasks, _ := a.Repo.Tasks()
	if len(tasks) != 0 {
		t.Fatal(tasks)
	}
	groups, err := a.TaskGroups()
	if err != nil || len(groups) != 0 {
		t.Fatal(groups, err)
	}
	// 表单预览应即时解释休眠，不能一直等到用户关闭窗口。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := a.ResolveName(ctx, sub); err == nil || ctx.Err() != nil {
		t.Fatal("名称预览应即时说明休眠", err)
	}
}
