package engine

import (
	"encoding/json"
	"math"
	"media-harbor/internal/domain"
	"regexp"
	"strconv"
	"strings"
)

// ProgressPrefix 仅用于进程与执行器之间传输结构化事件，不写入可读日志。
const ProgressPrefix = "@transfer "

func transfer(log func(string), value domain.TransferProgress) {
	raw, _ := json.Marshal(value)
	log(ProgressPrefix + string(raw))
}

var byteProgress = regexp.MustCompile(`\[download\]\s+([0-9.]+)%\s+of\s+~?\s*([0-9.]+)([KMGTP]?i?B)`)

// transferLog 将真实字节进度转为独立事件；合并视频的多个流累计计算。
func transferLog(log func(string), kind string, total int64) func(string) {
	var finished int64
	return func(line string) {
		log(line)
		m := byteProgress.FindStringSubmatch(line)
		if len(m) == 0 {
			return
		}
		percent, _ := strconv.ParseFloat(m[1], 64)
		size, _ := strconv.ParseFloat(m[2], 64)
		unit := strings.TrimSuffix(strings.TrimSuffix(m[3], "B"), "i")
		factor := float64(1)
		for i, u := range []string{"K", "M", "G", "T", "P"} {
			if unit == u {
				factor = math.Pow(1024, float64(i+1))
				break
			}
		}
		streamSize := int64(size * factor)
		if total <= 0 {
			total = streamSize
		}
		done := finished + int64(float64(streamSize)*percent/100)
		progress := float64(0)
		if total > 0 {
			progress = math.Min(99.9, float64(done)*100/float64(total))
		}
		transfer(log, domain.TransferProgress{Kind: kind, Status: "running", BytesTotal: total, BytesDone: done, Percent: progress})
		if strings.Contains(line, " in ") {
			finished += streamSize
		}
	}
}
