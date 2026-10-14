package engine

import (
	"context"
	"encoding/json"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 使用真实 gallery-dl 的 HTTP 下载器读取离线大文件，验证进度钩子而非模拟打印。
func TestInstagramHTTPProgress(t *testing.T) {
	// 正式 CLI、SDK 下载器和 Go 控制通道共同读取离线大文件。
	body := []byte("GIF89a" + strings.Repeat("x", 6*1024*1024-6))
	cli, address, _, _ := instagramFixture(t, false, 200, body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var items []domain.Content
	_, err := cli.Scan(ctx, domain.Subscription{Platform: "instagram", URL: address}, 10, func(string) {}, func(batch domain.ScanResult) error { items = append(items, batch.Items...); return nil })
	if err != nil || len(items) == 0 {
		t.Fatal(items, err)
	}
	root := t.TempDir()
	var events []domain.TransferProgress
	var output strings.Builder
	err = cli.Download(ctx, domain.Subscription{Platform: "instagram", URL: address}, items[0], domain.YouTubeOptions{}, root, func(line string) {
		output.WriteString(line + "\n")
		if strings.HasPrefix(line, ProgressPrefix) {
			var p domain.TransferProgress
			if e := json.Unmarshal([]byte(strings.TrimPrefix(line, ProgressPrefix)), &p); e != nil {
				t.Error(e)
			}
			events = append(events, p)
		}
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	// 响应头阶段允许零字节；随后必须观察到实际传输字节，且不能超过响应总量。
	var received bool
	for _, event := range events {
		if event.BytesTotal == 6*1024*1024 && event.BytesDone > 0 && event.BytesDone <= event.BytesTotal {
			received = true
		}
	}
	if !received {
		t.Fatalf("%+v", events)
	}
	info, err := os.Stat(filepath.Join(root, "fixture_1.gif"))
	if err != nil || info.Size() != 6*1024*1024 {
		t.Fatalf("file=%v err=%v", info, err)
	}
}
