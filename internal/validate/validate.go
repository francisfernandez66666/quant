// Package validate 多源行情交叉校验，确保不同数据源（API、Tushare 等）的报价在 0.1% 容差内一致。
package validate

import (
	"fmt"
	"math"

	"quant-trading/internal/config"
)

// Engine 交叉校验引擎，持有配置管理器。
type Engine struct {
	cfg *config.Manager
}

// New 创建校验引擎实例。
func New(cfg *config.Manager) *Engine {
	return &Engine{cfg: cfg}
}

// PriceSource 数据源标识类型。
type PriceSource string

const (
	SourceAPI     PriceSource = "bottom_api"
	SourceTushare PriceSource = "tushare"
	SourceCross   PriceSource = "cross_validate"
	SourceManual  PriceSource = "manual"
)

// ValidationResult 校验结果，标明是否一致、最终取值、最大差异和是否告警。
type ValidationResult struct {
	Consistent bool    `json:"consistent"`
	Value      float64 `json:"value"`
	Source     string  `json:"source"`
	Diff       float64 `json:"diff"`
	Alert      bool    `json:"alert"`
}

// CrossCheck 对多个数据源的报价做两两交叉比对，最大差异 <= 0.1% 则认为一致，返回均值。
func (e *Engine) CrossCheck(sources map[PriceSource]float64) *ValidationResult {
	values := make([]float64, 0, len(sources))
	sourceNames := make([]string, 0, len(sources))
	for name, val := range sources {
		values = append(values, val)
		sourceNames = append(sourceNames, string(name))
	}
	if len(values) < 2 {
		return &ValidationResult{
			Consistent: true,
			Value:      values[0],
			Source:     sourceNames[0],
			Alert:      false,
		}
	}
	maxDiff := 0.0
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			diff := math.Abs(values[i]-values[j]) / math.Max(math.Abs(values[i]), math.Abs(values[j]))
			if diff > maxDiff {
				maxDiff = diff
			}
		}
	}
	if maxDiff <= 0.001 {
		mean := 0.0
		for _, v := range values {
			mean += v
		}
		mean /= float64(len(values))
		return &ValidationResult{
			Consistent: true,
			Value:      mean,
			Source:     "cross_validate",
			Diff:       maxDiff,
			Alert:      false,
		}
	}
	return &ValidationResult{
		Consistent: false,
		Value:      0,
		Source:     "cross_validate_fail",
		Diff:       maxDiff,
		Alert:      true,
	}
}

// CheckDataHealth 检查数据源配置是否至少有一个启用。
func (e *Engine) CheckDataHealth() error {
	cfg := e.cfg.Get()
	if cfg.DataSource.BottomAPI.Enabled && cfg.DataSource.Tushare.Enabled {
		return nil
	}
	if cfg.DataSource.BottomAPI.Enabled || cfg.DataSource.Tushare.Enabled {
		return nil
	}
	return fmt.Errorf("no data source enabled")
}

// ForceCheck 强制校验指定场景是否在白名单中，用于下单/止损/止盈/仓位计算前把关。
func (e *Engine) ForceCheck(scenario string) error {
	scenarios := map[string]bool{
		"before_order":         true,
		"before_stop_loss":     true,
		"before_take_profit":   true,
		"before_position_calc": true,
	}
	if !scenarios[scenario] {
		return fmt.Errorf("unknown scenario: %s", scenario)
	}
	return nil
}
