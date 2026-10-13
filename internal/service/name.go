package service

import (
	"context"
	"fmt"
	"media-harbor/internal/domain"
	"strings"
	"time"
)

// ResolveName 为新建表单提供作者名称，独立于订阅扫描与任务队列。
// 引擎接口隔离元数据来源，返回的名称只能作为一个目录段。
func (a *App) ResolveName(ctx context.Context, s domain.Subscription) (string, error) {
	resolver, ok := a.Engine.(domain.NameResolver)
	if !ok {
		return "", fmt.Errorf("当前引擎不支持作者名称解析")
	}
	name, err := resolver.ResolveName(ctx, s)
	if err != nil {
		return "", err
	}
	return strings.NewReplacer("/", "_", "\\", "_").Replace(strings.TrimSpace(name)), nil
}

// CreateSubscription 在 URL 解析阶段取得作者身份，保存配置后立即获取头像。
// 未保存表单不落头像；外部解析与下载不持有任务队列锁。
func (a *App) CreateSubscription(ctx context.Context, s *domain.Subscription) error {
	started := time.Now()
	var resolveErr error
	// 身份来自引擎解析，忽略客户端提交的稳定 ID。
	s.StableID, s.CurrentName = "", ""
	if s.Platform == "instagram" {
		if resolver, ok := a.Engine.(domain.AuthorResolver); ok {
			author, err := resolver.ResolveAuthor(ctx, *s)
			resolveErr = err
			if err == nil {
				s.StableID, s.CurrentName = author.StableID, author.CurrentName
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := a.SaveSubscription(s); err != nil {
		return err
	}
	if s.Platform != "instagram" {
		return nil
	}
	// 创建阶段日志归属已保存的任务配置，头像失败不撤销任务创建。
	writer, err := a.beginTaskLog(*s, started)
	if err != nil {
		return err
	}
	defer a.endTaskLog(*s, started, writer)
	log := func(message string) { a.writeTaskLog(writer, domain.Task{SubscriptionID: s.ID}, message) }
	log(fmt.Sprintf("创建解析阶段：任务 %d；作者 %s；稳定 ID=%s", s.ID, s.CurrentName, s.StableID))
	if resolveErr != nil {
		log("头像作者解析失败：" + resolveErr.Error())
	} else {
		a.updateCreationAvatar(ctx, *s, log)
	}
	log(fmt.Sprintf("创建解析阶段结束；耗时 %s", time.Since(started).Round(time.Millisecond)))
	return nil
}
