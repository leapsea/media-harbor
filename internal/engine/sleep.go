package engine

import (
	"context"
	"fmt"
	"media-harbor/internal/config"
	"media-harbor/internal/domain"
	"time"
)

// configureSleep 唤醒所有等待者；关闭或缩短休眠立即生效。
func (r *instagramRequests) configureSleep(s domain.SleepSettings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sleep = s
	close(r.changed)
	r.changed = make(chan struct{})
}

// waitAwake 用于进程启动和消费者认领；已有媒体传输不经过此等待。
// 定期报告暂停阶段，取消不等待休眠结束，也不占用应用服务锁。
func (r *instagramRequests) waitAwake(ctx context.Context, log func(string)) error {
	waiting := false
	for {
		// 取消先于窗口判断，已取消任务不能重新发起平台请求。
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.checkCredentials(); err != nil {
			return err
		}
		r.mu.Lock()
		until, changed := r.sleep.Until(time.Now()), r.changed
		r.mu.Unlock()
		// 保留原SDK进程，唤醒后继续当前请求，不重新枚举历史。
		if until.IsZero() {
			if waiting {
				log("休眠结束，继续原任务")
			}
			return nil
		}
		waiting = true
		log(fmt.Sprintf("休眠中；暂停生产与新下载；恢复时间 %s", until.Format(time.RFC3339)))
		// 设置变化可即时唤醒，长窗口仅周期输出等待阶段。
		timer := time.NewTimer(min(time.Until(until), config.ScanLogInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
