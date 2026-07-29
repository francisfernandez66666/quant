//go:build android

package agent

import (
	"context"

	"quant-trading/internal/registry"
)

// RegisterAll 注册安卓版裁剪 agent。
func RegisterAll(m *Manager, r *registry.Registry, p *Params) {
	registry.RegisterAll(r, p)

	m.Register("warmup", func() error {
		if err := r.StartCritical(context.Background()); err != nil {
			return err
		}
		return r.StartBusiness(context.Background(), 4)
	})
}
