package engine

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strings"
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

// 真实格式决定保存后缀，SDK文件名错误及图片格式切换都不能留下旧路径。
func TestAvatarUsesImageBytesAndRemovesPreviousFormat(t *testing.T) {
	root := t.TempDir()
	sub := domain.Subscription{Platform: "instagram", StableID: "format", CurrentName: "author"}
	path := AvatarPath(root, sub)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	var pngBody bytes.Buffer
	if err := png.Encode(&pngBody, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	downloaded := filepath.Join(t.TempDir(), "avatar.jpg")
	if err := os.WriteFile(downloaded, pngBody.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := replaceAvatar(path, downloaded, int64(pngBody.Len()), "author", time.Now(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	actual := AvatarPath(root, sub)
	if filepath.Ext(actual) != ".png" {
		t.Fatal(actual)
	}
	// GIF 内容长度与 PNG 不同，更新后只剩下当前图片。
	gifBody := []byte("GIF89a" + strings.Repeat("g", pngBody.Len()+1))
	downloaded = filepath.Join(t.TempDir(), "avatar.gif")
	if err := os.WriteFile(downloaded, gifBody, 0644); err != nil {
		t.Fatal(err)
	}
	if err := replaceAvatar(actual, downloaded, int64(len(gifBody)), "author", time.Now(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(AvatarPath(root, sub)) != ".gif" {
		t.Fatal(AvatarPath(root, sub))
	}
	if _, err := os.Stat(actual); !os.IsNotExist(err) {
		t.Fatal("旧格式头像仍残留", err)
	}
}

// 旧img只重命名，不重新下载、改写内容或更改时间；重复执行仍使用同一文件。
func TestLegacyAvatarNormalizationPreservesFile(t *testing.T) {
	root := t.TempDir()
	sub := domain.Subscription{Platform: "instagram", StableID: "legacy"}
	old := strings.TrimSuffix(AvatarPath(root, sub), ".jpg") + ".img"
	if err := os.MkdirAll(filepath.Dir(old), 0755); err != nil {
		t.Fatal(err)
	}
	// WebP头部由HTTP内容识别器确认，与旧文件名无关。
	body := []byte("RIFF\x10\x00\x00\x00WEBPVP8 " + strings.Repeat("x", 16))
	if err := os.WriteFile(old, body, 0644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1000, 0)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	path, err := NormalizeAvatar(old)
	if err != nil || filepath.Ext(path) != ".webp" {
		t.Fatal(path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, body) {
		t.Fatal("内容被修改", err)
	}
	info, _ := os.Stat(path)
	if !info.ModTime().Equal(stamp) {
		t.Fatal("文件时间被修改")
	}
	if AvatarPath(root, sub) != path {
		t.Fatal("新头像查找失败")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("旧img未迁移", err)
	}
	again, err := NormalizeAvatar(path)
	if err != nil || again != path {
		t.Fatal(again, err)
	}
}
