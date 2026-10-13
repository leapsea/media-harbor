package storage

import (
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"path/filepath"
	"testing"
	"time"
)

// TestCancelSubscriptionTasksAtomicScope 验证活动集合、内容状态和保留的下载信息。
func TestCancelSubscriptionTasksAtomicScope(t *testing.T) {
	// 真实 SQLite 验证事务与 JSON 字段，不用模拟仓储掩盖写入顺序问题。
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 只有前两个状态可被取消，其他结束状态应保留原结果。
	for i, status := range []string{"queued", "running", "completed", "failed", "cancelled", "skipped"} {
		if err = s.AddContentAndTask(1, domain.Content{RemoteID: status}); err != nil {
			t.Fatal(err)
		}
		tasks, _ := s.SubscriptionTasks(1)
		task := tasks[0]
		task.Status, task.Attempts, task.Error = status, i, "保留原始错误"
		// 进度和尝试次数属于历史，取消只修改状态和结束时间。
		task.Progress = []domain.TransferProgress{{Kind: "video", BytesDone: 42}}
		if err = s.SaveTask(&task); err != nil {
			t.Fatal(err)
		}
		// 已下载路径与错误也必须保留，重试才有继续执行的依据。
		content, _ := s.Content(task.ContentID)
		content.Status, content.Paths, content.Error = status, []string{"/downloads/existing.mp4"}, "保留内容错误"
		if err = s.SaveContent(&content); err != nil {
			t.Fatal(err)
		}
	}
	// 其他订阅作为隔离对照，扫描与下载属于同一个取消范围。
	other, err := s.Enqueue(2, 0, "scan")
	if err != nil {
		t.Fatal(err)
	}
	scan, err := s.Enqueue(1, 0, "scan")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.SubscriptionTasks(1)
	ids, err := s.CancelSubscriptionTasks(1)
	if err != nil || len(ids) != 3 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	// 对照操作前快照逐项核对，防止批量 SQL 丢失不相关字段。
	for _, old := range before {
		current, _ := s.Task(old.ID)
		active := old.Status == "queued" || old.Status == "running"
		want := old.Status
		if active {
			want = "cancelled"
		}
		if current.Status != want || current.Attempts != old.Attempts || current.Error != old.Error || len(current.Progress) != len(old.Progress) {
			t.Fatalf("任务信息被改写: before=%+v after=%+v", old, current)
		}
		// 取消已经持久化，汇总列表才能显示正确的最近更新时间。
		if active && current.FinishedAt.IsZero() {
			t.Fatal("取消时间未保存")
		}
		if old.ContentID == 0 {
			continue
		}
		content, _ := s.Content(old.ContentID)
		if content.Status != want || len(content.Paths) != 1 || content.Error != "保留内容错误" {
			t.Fatal(content)
		}
	}
	if current, _ := s.Task(other.ID); current.Status != "queued" {
		t.Fatal(current)
	}
	if current, _ := s.Task(scan.ID); current.Status != "cancelled" {
		t.Fatal(current)
	}
	// 重复点击不重新改写已结束记录，也不能再次发出停止信号。
	if ids, err = s.CancelSubscriptionTasks(1); err != nil || len(ids) != 0 {
		t.Fatalf("重复取消: %v %v", ids, err)
	}
}

// BenchmarkSubscriptionCancellation 对比历史逐条写入与事务批量写入，只使用合成队列。
// 重置和建库不计入操作耗时，不访问本地容器中的真实历史或文件。
func BenchmarkSubscriptionCancellation(b *testing.B) {
	// 两条路径使用相同规模的独立合成数据，避免真实历史影响结果。
	for _, mode := range []string{"逐条取消", "事务取消"} {
		b.Run(mode, func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "media.db"), config.Defaults())
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			// 队列创建不进入计时，测量对象仅为取消操作本身。
			for i := range 1000 {
				if err = s.AddContentAndTask(1, domain.Content{RemoteID: fmt.Sprint(i)}); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for range b.N {
				// 每轮恢复待执行状态，使多轮基准仍取消同样多的条目。
				b.StopTimer()
				if _, err = s.db.Exec("UPDATE tasks SET status='queued',payload=json_set(payload,'$.status','queued')"); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				// 同时核对受影响数量，避免空操作被误当成更快的实现。
				if mode == "事务取消" {
					ids, err := s.CancelSubscriptionTasks(1)
					if err != nil || len(ids) != 1000 {
						b.Fatal(len(ids), err)
					}
					continue
				}
				// 保留历史路径用于耗时比较：每条重新读取任务、内容并分别写入。
				tasks, err := s.Tasks()
				if err != nil {
					b.Fatal(err)
				}
				for _, item := range tasks {
					task, err := s.Task(item.ID)
					if err != nil {
						b.Fatal(err)
					}
					content, err := s.Content(task.ContentID)
					if err != nil {
						b.Fatal(err)
					}
					content.Status = "cancelled"
					if err = s.SaveContent(&content); err != nil {
						b.Fatal(err)
					}
					task.Status, task.FinishedAt = "cancelled", time.Now()
					if err = s.SaveTask(&task); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// TestCancelSubscriptionTasksRollback 在任务更新失败时，内容状态也必须回滚。
func TestCancelSubscriptionTasksRollback(t *testing.T) {
	// 使用异常 JSON 触发真实 SQLite 写入失败，不引入数据库触发器。
	s, err := Open(filepath.Join(t.TempDir(), "media.db"), config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AddContentAndTask(1, domain.Content{RemoteID: "rollback"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.SubscriptionTasks(1)
	if _, err = s.db.Exec("UPDATE tasks SET payload='invalid json' WHERE id=?", tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	// 第二步失败时，第一步已更新的内容也必须恢复为待执行。
	if _, err = s.CancelSubscriptionTasks(1); err == nil {
		t.Fatal("损坏快照应返回失败")
	}
	content, err := s.Content(tasks[0].ContentID)
	if err != nil || content.Status != "queued" {
		t.Fatalf("内容没有回滚: %+v %v", content, err)
	}
	var status string
	if err = s.db.QueryRow("SELECT status FROM tasks WHERE id=?", tasks[0].ID).Scan(&status); err != nil || status != "queued" {
		t.Fatal(status, err)
	}
}
