package service

import (
	"context"
	"errors"
	"fmt"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCatchupReconcilesDownloads 覆盖重新枚举、新增、失败恢复、完成跳过和重复排队。
// 使用真实数据库验证任务 ID 与内容状态，媒体下载由可控引擎提供。
func TestCatchupReconcilesDownloads(t *testing.T) {
	a := testApp(t, manyEngine{})
	s := addSub(t, a, "catchup", "instagram")
	states := []string{"completed", "failed", "cancelled", "queued", "running"}
	for i := range states {
		if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: fmt.Sprint(i + 1), URL: "https://example.com/old"}); err != nil {
			t.Fatal(err)
		}
	}
	contents, _ := a.Repo.Contents()
	tasks, _ := a.Repo.Tasks()
	originalIDs := map[string]int64{}
	for _, c := range contents {
		var index int
		fmt.Sscan(c.RemoteID, &index)
		c.Status = states[index-1]
		c.Error = "previous error"
		if c.Status == "completed" {
			path := filepath.Join(t.TempDir(), "completed.mp4")
			_ = os.WriteFile(path, []byte("media"), 0644)
			c.Paths = []string{path}
			c.Archived = true
			c.Error = ""
		}
		if err := a.Repo.SaveContent(&c); err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			if task.ContentID == c.ID {
				task.Status = c.Status
				task.Attempts = 1
				task.Error = c.Error
				if err := a.Repo.SaveTask(&task); err != nil {
					t.Fatal(err)
				}
				originalIDs[c.RemoteID] = task.ID
			}
		}
	}
	// 补全下载扫描自身只创建一条任务；现有等待/运行条目保持原状。
	scan, err := a.QueueScan(s.ID, "catchup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.QueueScan(s.ID, "catchup"); err == nil {
		t.Fatal("duplicate scan accepted")
	}
	scan.Status = "running"
	if err = a.Repo.SaveTask(&scan); err != nil {
		t.Fatal(err)
	}
	settings, _ := a.Repo.Settings()
	a.execute(context.Background(), scan, s, settings)
	after, _ := a.Repo.Contents()
	if len(after) != 15 {
		t.Fatalf("content count=%d", len(after))
	}
	afterTasks, _ := a.Repo.Tasks()
	if len(afterTasks) != 16 {
		t.Fatalf("task count=%d", len(afterTasks))
	}
	for _, c := range after {
		switch c.RemoteID {
		case "1":
			if c.Status != "completed" || !c.Archived || len(c.Paths) != 1 {
				t.Fatalf("completed changed: %+v", c)
			}
		case "2", "3":
			if c.Status != "queued" || c.Error != "" || c.URL == "https://example.com/old" {
				t.Fatalf("retry not refreshed: %+v", c)
			}
		case "4":
			if c.Status != "queued" {
				t.Fatalf("queued changed: %+v", c)
			}
		case "5":
			if c.Status != "running" {
				t.Fatalf("running changed: %+v", c)
			}
		}
	}
	// 失败和取消复用任务身份及尝试次数，补全下载本身不冒充下载执行。
	for _, task := range afterTasks {
		for remote, id := range originalIDs {
			if task.ID == id && (remote == "2" || remote == "3") && (task.Status != "queued" || task.Attempts != 1 || task.Error != "") {
				t.Fatalf("retry task=%+v", task)
			}
		}
	}
	raw, _ := os.ReadFile(taskLogPathForTest(t, a, scan))
	want := "补全下载发现 15 项；新增下载 10 项；重新排队 2 项；已完成跳过 1 项；处理中跳过 2 项"
	if !strings.Contains(string(raw), want) {
		t.Fatalf("log=%s", raw)
	}
	// 第二次补全下载仍重新枚举，但所有已排队内容都不得再产生下载任务。
	next, err := a.QueueScan(s.ID, "catchup")
	if err != nil {
		t.Fatal(err)
	}
	next.Status = "running"
	_ = a.Repo.SaveTask(&next)
	a.execute(context.Background(), next, s, settings)
	final, _ := a.Repo.Tasks()
	if len(final) != 17 {
		t.Fatalf("duplicate downloads created: %d", len(final))
	}
}

// TestCatchupLimited 保证补全下载也遵循测试上限，不为验收处理全部历史。
func TestCatchupLimited(t *testing.T) {
	a := testApp(t, manyEngine{})
	settings, _ := a.Repo.Settings()
	settings.Debug.TestItemLimit = 10
	if err := a.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	s := addSub(t, a, "limited", "instagram")
	task, err := a.QueueScan(s.ID, "catchup")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	contents, _ := a.Repo.Contents()
	if len(contents) != 10 {
		t.Fatalf("limited content=%d", len(contents))
	}
	raw, _ := os.ReadFile(taskLogPathForTest(t, a, task))
	if !strings.Contains(string(raw), "补全下载发现 10 项") {
		t.Fatalf("log=%s", raw)
	}
}

// TestCatchupPartialScan 仅处理实际枚举到的内容，不能恢复平台本次不可见的条目。
// 发现的媒体可以恢复，但分页错误仍使补全下载扫描失败，而不是宣称成功。
func TestCatchupPartialScan(t *testing.T) {
	a := testApp(t, fakeEngine{scanError: errors.New("page failed")})
	s := addSub(t, a, "partial", "instagram")
	for _, remote := range []string{"one", "invisible"} {
		if err := a.Repo.AddContentAndTask(s.ID, domain.Content{RemoteID: remote, URL: "https://example.com/old"}); err != nil {
			t.Fatal(err)
		}
	}
	contents, _ := a.Repo.Contents()
	for _, c := range contents {
		c.Status = "failed"
		if err := a.Repo.SaveContent(&c); err != nil {
			t.Fatal(err)
		}
	}
	tasks, _ := a.Repo.Tasks()
	for _, task := range tasks {
		task.Status = "failed"
		if err := a.Repo.SaveTask(&task); err != nil {
			t.Fatal(err)
		}
	}
	scan, err := a.QueueScan(s.ID, "catchup")
	if err != nil {
		t.Fatal(err)
	}
	scan.Status = "running"
	_ = a.Repo.SaveTask(&scan)
	// 直接执行扫描，避免先运行已有下载任务影响断言中的原始失败状态。
	settings, _ := a.Repo.Settings()
	a.execute(context.Background(), scan, s, settings)
	current, _ := a.Repo.Task(scan.ID)
	if current.Status != "failed" || current.Error != "page failed" {
		t.Fatalf("scan=%+v", current)
	}
	contents, _ = a.Repo.Contents()
	for _, c := range contents {
		if (c.RemoteID == "one" && c.Status != "queued") || (c.RemoteID == "invisible" && c.Status != "failed") {
			t.Fatalf("content=%+v", c)
		}
	}
	tasks, _ = a.Repo.Tasks()
	if len(tasks) != 3 {
		t.Fatalf("unexpected tasks=%d", len(tasks))
	}
}
