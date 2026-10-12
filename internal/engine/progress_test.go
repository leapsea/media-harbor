package engine

import (
	"encoding/json"
	"media-harbor/internal/domain"
	"strings"
	"testing"
)

// 两个流的字节累加不能在主视频的第一个流结束时宣告整项完成。
func TestTransferProgressStreams(t *testing.T) {
	var events []domain.TransferProgress
	log := transferLog(func(line string) {
		if strings.HasPrefix(line, ProgressPrefix) {
			var p domain.TransferProgress
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, ProgressPrefix)), &p); err != nil {
				t.Fatal(err)
			}
			events = append(events, p)
		}
	}, "video", 12*1024*1024)
	log("[download] 100% of 10.00MiB in 00:10 at 1MiB/s")
	log("[download] 50.0% of 2.00MiB at 1MiB/s ETA 00:01")
	if len(events) != 2 || events[1].BytesDone != 11*1024*1024 || events[1].Percent >= 100 || events[1].Status != "running" {
		t.Fatalf("%+v", events)
	}
	log("[download] 100% of 2.00MiB in 00:01 at 2MiB/s")
	if events[2].Percent != 99.9 {
		t.Fatalf("completion before postprocessing: %+v", events[2])
	}
}

func TestTransferSizeAndUnknown(t *testing.T) {
	raw := []byte(`{"requested_formats":[{"filesize":6000000},{"filesize_approx":1000000}],"formats":[{"vcodec":"none","filesize":2000000}]}`)
	if youtubeTransferSize(raw, "video") != 7000000 || youtubeTransferSize(raw, "audio") != 2000000 || youtubeTransferSize(nil, "video") != 0 {
		t.Fatal("wrong size")
	}
	// 零字节的引擎信息不能输出 NaN/Inf，也不能使 JSON 编码丢失整个事件。
	var event string
	transferLog(func(line string) {
		if strings.HasPrefix(line, ProgressPrefix) {
			event = line
		}
	}, "audio", 0)("[download] 0.0% of 0.00B ETA 00:01")
	if !strings.Contains(event, `"percent":0`) {
		t.Fatal(event)
	}
}
