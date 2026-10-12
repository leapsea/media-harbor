// Command server 是配置和适配器的唯一装配入口。
package main

import (
	"context"
	"errors"
	"log/slog"
	"media-harbor/internal/config"
	"media-harbor/internal/engine"
	"media-harbor/internal/httpapi"
	"media-harbor/internal/service"
	"media-harbor/internal/storage"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// main 使用构造函数依赖注入，单实例无需额外容器框架。
func main() {
	if e := run(); e != nil {
		slog.Error("应用停止", "error", e)
		os.Exit(1)
	}
}

// 数据目录不可初始化时不启动依赖数据库的后台服务。
// 下载和归档目录的状态由应用单独探测。
// 两个后台循环共享信号 Context，退出顺序由入口统一控制。
func run() error {
	c := config.Load()
	for _, dir := range []string{c.DataDir, c.LogDir, filepath.Join(c.DataDir, "cookies")} {
		if e := os.MkdirAll(dir, 0755); e != nil {
			return e
		}
	}
	// 服务日志进入标准输出，任务输出另存 data 下的独立文件。
	db, e := storage.Open(filepath.Join(c.DataDir, "media.db"), service.InitialSettings(c))
	if e != nil {
		return e
	}
	defer db.Close()
	app := service.New(c, db, engine.CLI{DataDir: c.DataDir, PaidPolicy: db})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() { defer close(done); app.Run(ctx) }()
	server := &http.Server{Addr: c.Address, Handler: httpapi.Handler(app)}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("Media Harbor V1 已启动", "address", c.Address)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case e = <-errCh:
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		stop()
	}
	// 停止认领新任务，取消运行中的引擎，然后关闭持久化连接。
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	<-done
	return e
}
