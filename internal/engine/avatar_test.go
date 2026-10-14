package engine

import (
	"context"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAvatarSizePolicy 同一博主即使更名也共用文件；相同字节数保留原内容和时间。
func TestAvatarSizePolicy(t *testing.T) {
	root := t.TempDir()
	command := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 case "$1" in
 --directory) shift; destination="$1" ;;
 esac
 shift
done
if [ "$AVATAR_FAILURE" = 1 ]; then exit 1; fi
printf '%s' "$AVATAR_BODY" > "$destination/avatar.jpg"
`
	// 正式入口使用 Python SDK 适配，替身只负责产生头像文件。
	if err := os.WriteFile(filepath.Join(root, "python3"), []byte(command), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	cli := CLI{DataDir: root}
	downloads := filepath.Join(root, "downloads")
	sub := domain.Subscription{Platform: "instagram", StableID: "123", CurrentName: "alice"}
	log := func(string) {}
	t.Setenv("AVATAR_BODY", "first")
	if err := cli.UpdateAvatar(context.Background(), sub, AvatarPath(downloads, sub), log); err != nil {
		t.Fatal(err)
	}
	path := AvatarPath(downloads, sub)
	if _, err := os.Stat(AvatarPath(cli.DataDir, sub)); !os.IsNotExist(err) {
		t.Fatal("头像写入了 data 目录", err)
	}
	stamp := time.Unix(1000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	sub.CurrentName = "renamed"
	t.Setenv("AVATAR_BODY", "other")
	if err := cli.UpdateAvatar(context.Background(), sub, AvatarPath(downloads, sub), log); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "first" || !info.ModTime().Equal(stamp) {
		t.Fatal("同大小头像覆盖了原文件")
	}
	t.Setenv("AVATAR_BODY", "different size")
	if err := cli.UpdateAvatar(context.Background(), sub, AvatarPath(downloads, sub), log); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "different size" {
		t.Fatal("大小变化未替换头像")
	}
	t.Setenv("AVATAR_FAILURE", "1")
	if err := cli.UpdateAvatar(context.Background(), sub, AvatarPath(downloads, sub), log); err == nil {
		t.Fatal("头像下载失败没有返回错误")
	}
	data, _ = os.ReadFile(path)
	if string(data) != "different size" {
		t.Fatal("失败破坏了已有头像")
	}
}
