package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seedDetailHistory 直接构造当前帖子任务与下载资源任务与执行账本，避免为性能样本启动下载或逐条聚合。
// 目标订阅有千条历史，其余订阅的尝试模拟全站账本增长；界面仍只读取前十条。
func seedDetailHistory(t testing.TB, s *Store, sid int64) {
	t.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO post_tasks(id,subscription_id,remote_id,payload) VALUES(?,?,?,?)`,
		`INSERT INTO content_items(id,subscription_id,remote_id,payload) VALUES(?,?,?,?)`,
		`INSERT INTO tasks(id,subscription_id,content_id,kind,status,payload) VALUES(?,?,?,'download','completed',?)`,
	}
	for n := 1; n <= 1000; n++ {
		payload := fmt.Sprintf(`{"id":%d,"remote_id":"video-%d"}`, n, n)
		for _, q := range statements[:2] {
			if _, err = tx.Exec(q, n, sid, fmt.Sprint(n), payload); err != nil {
				t.Fatal(err)
			}
		}
		for _, kind := range []string{"video", "metadata", "comments", "subtitles", "thumbnail"} {
			if _, err = tx.Exec(`INSERT INTO download_items(post_id,content_id,kind,payload) VALUES(?,?,?,?)`, n, n, kind, fmt.Sprintf(`{"required":true,"kind":%q,"content_id":%d}`, kind, n)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.Exec(statements[2], n, sid, n, payload); err != nil {
			t.Fatal(err)
		}
	}
	for n := 1001; n <= 11000; n++ {
		if _, err = tx.Exec(statements[2], n, sid+1, n, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// 新尝试不能改变前十条的发现顺序；一个视频的全部必需下载资源任务仍关联同一次最新执行。
// 不同订阅的海量历史、扫描任务与非必需子项均不进入详情。
func TestPostDetailsLatestAttemptAndScope(t *testing.T) {
	s, sub := postStore(t, "youtube")
	seedDetailHistory(t, s, sub.ID)
	stamp := time.Date(2026, 10, 14, 12, 0, 0, 123, time.UTC)
	for _, q := range []string{
		`INSERT INTO tasks(id,subscription_id,content_id,kind,status,payload) VALUES(12000,1,1,'download','running','{"created_at":"2026-10-14T12:00:00.000000123Z","started_at":"2026-10-14T12:00:00.000000123Z"}')`,
		`INSERT INTO tasks(id,subscription_id,content_id,kind,status,payload) VALUES(12001,1,0,'scan','completed','{}')`,
		`INSERT INTO download_items(post_id,content_id,kind,payload) VALUES(1,1,'audio','{"required":false}')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.PostDownloadRows(context.Background(), sub.ID, 10)
	if err != nil || len(rows) != 50 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if row.Post.ID > 10 || !row.Download.Required {
			t.Fatalf("unexpected scope: %+v", row)
		}
		if row.Content.ID == 1 && (row.Task.ID != 12000 || !row.Task.StartedAt.Equal(stamp) || !row.Task.CreatedAt.Equal(stamp)) {
			t.Fatalf("latest attempt: %+v", row.Task)
		}
	}
}

// 占住唯一连接模拟下载写事务；关闭请求必须结束等待，不需要先释放下载事务。
func TestPostDetailsCancellationWhileWaitingForDatabase(t *testing.T) {
	s, sub := postStore(t, "youtube")
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = s.PostDownloadRows(ctx, sub.ID, 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query did not cancel its connection wait: %v", err)
	}
}

// BenchmarkPostDetailsHistory 测量真实查询与 JSON 解码，包含调试十条与完整历史两种读取。
// 只构造数据库状态，不枚举或下载平台媒体。
func BenchmarkPostDetailsHistory(b *testing.B) {
	s, sub := postStore(b, "youtube")
	seedDetailHistory(b, s, sub.ID)
	for _, limit := range []int{10, 0} {
		b.Run(fmt.Sprintf("limit=%d", limit), func(b *testing.B) {
			for b.Loop() {
				rows, err := s.PostDownloadRows(context.Background(), sub.ID, limit)
				want := 5000
				if limit > 0 {
					want = limit * 5
				}
				if err != nil || len(rows) != want {
					b.Fatalf("rows=%d err=%v", len(rows), err)
				}
			}
		})
	}
}
