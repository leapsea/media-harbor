package service

import (
	"context"
	"fmt"
	"media-harbor/internal/domain"
	"strings"
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
