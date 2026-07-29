//go:build !android

package agent

import (
	"context"

	"quant-trading/internal/registry"
)

// RegisterAll 注册桌面版全量 agent。
// adapter 创建委托给 registry.RegisterAll，agent 只控制启动时机。
func RegisterAll(m *Manager, r *registry.Registry, p *Params) {
	registry.RegisterAll(r, p)

	// 单个 warmup agent 保证顺序：Critical 先 → Business 后
	m.Register("warmup", func() error {
		if err := r.StartCritical(context.Background()); err != nil {
			return err
		}
		return r.StartBusiness(context.Background(), 4)
	})
}
