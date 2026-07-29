// Package position 持仓管理器，负责从 JSON 文件读写用户持仓数据。
package position

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UserHolding 单只持仓记录。
type UserHolding struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Quantity      int     `json:"quantity"`
	CostPrice     float64 `json:"cost_price"`
	CurPrice      float64 `json:"cur_price,omitempty"`
	PnlPct        float64 `json:"pnl_pct,omitempty"`
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"`
	StopLossPct   float64 `json:"stop_loss_pct,omitempty"`

	EntryStrategy string             `json:"entry_strategy,omitempty"`
	EntryAt       string             `json:"entry_at,omitempty"`
	EntryMeta     map[string]float64 `json:"entry_meta,omitempty"`
}

// UserHoldings 完整持仓数据，含更新时间、可用余额和持仓列表。
type UserHoldings struct {
	UpdatedAt        time.Time     `json:"updated_at"`
	AvailableBalance float64       `json:"available_balance"`
	Holdings         []UserHolding `json:"holdings"`
}

// HoldingsManager 持仓管理器，线程安全地读写 JSON 文件。
type HoldingsManager struct {
	mu   sync.RWMutex
	data UserHoldings
	path string
}

// NewHoldingsManager 创建持仓管理器，自动从指定目录加载持仓文件。
func NewHoldingsManager(dir string) *HoldingsManager {
	path := filepath.Join(dir, "holdings.json")
	m := &HoldingsManager{path: path}
	m.load()
	return m
}

func (m *HoldingsManager) load() {
	data, err := os.ReadFile(m.path)
	if err != nil {
		log.Printf("持仓文件不存在 %s, 使用空持仓", m.path)
		return
	}
	var h UserHoldings
	if err := json.Unmarshal(data, &h); err != nil {
		log.Printf("持仓文件解析失败: %v", err)
		return
	}
	m.data = h
	log.Printf("已加载 %d 条持仓记录", len(h.Holdings))
}

// Get 返回当前持仓数据（读锁）。
func (m *HoldingsManager) Get() UserHoldings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data
}

// Set 更新持仓数据，写入 JSON 文件持久化（写锁）。
func (m *HoldingsManager) Set(h UserHoldings) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	h.UpdatedAt = time.Now()
	m.data = h

	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.path, data, 0644); err != nil {
		return err
	}
	log.Printf("持仓已保存: %d 只, 可用 %.2f", len(h.Holdings), h.AvailableBalance)
	return nil
}
