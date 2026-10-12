package engine

import (
	"context"
	"errors"
	"media-harbor/internal/domain"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type paidPolicy int64

func (p paidPolicy) PaidFailure(string) (int64, error) { return int64(p), nil }

// 付费元数据即使无格式也保留作者身份，后续策略在主视频和附属项之前生效。
func TestPaidYouTubeMetadata(t *testing.T) {
	raw := []byte(`{"id":"paid","channel_id":"UCpaid","availability":"subscriber_only","formats":[]}`)
	scan, err := parseYouTube(raw)
	if err != nil || len(scan.Items) != 1 || scan.Items[0].ChannelID != "UCpaid" || !domain.IsPaidAvailability(scan.Items[0].Availability) {
		t.Fatalf("%+v %v", scan, err)
	}
	root := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' '{\"id\":\"paid\",\"channel_id\":\"UCpaid\",\"availability\":\"subscriber_only\",\"formats\":[]}'\n"
	_ = os.WriteFile(filepath.Join(root, "yt-dlp"), []byte(script), 0755)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, blocked := range []paidPolicy{0, 42} {
		dir := t.TempDir()
		var messages []string
		err = (CLI{PaidPolicy: blocked}).Download(context.Background(), domain.Subscription{Platform: "youtube"}, domain.Content{URL: "https://example.com/paid"}, domain.YouTubeOptions{Video: true, Format: "best", Comments: true}, dir, func(s string) { messages = append(messages, s) })
		if blocked == 0 {
			if err == nil || !strings.Contains(err.Error(), "平台未提供媒体格式") {
				t.Fatal(err)
			}
		} else {
			var skipped *domain.PaidVideoSkippedError
			if !errors.As(err, &skipped) || skipped.FailedTaskID != 42 {
				t.Fatal(err)
			}
		}
		if strings.Contains(strings.Join(messages, "\n"), "附属任务 [") {
			t.Fatal(messages)
		}
		channel, availability := PublicationAccess(dir)
		if channel != "UCpaid" || availability != "subscriber_only" {
			t.Fatalf("%s %s", channel, availability)
		}
	}
	for _, value := range []string{"public", "private", "needs_auth", "unlisted", ""} {
		if domain.IsPaidAvailability(value) {
			t.Fatal(value)
		}
	}
}
