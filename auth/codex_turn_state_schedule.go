package auth

import "strings"

// CodexTurnStateScheduleCredentialKey 是账号定时取件配置在 credentials 中的存储键。
const CodexTurnStateScheduleCredentialKey = "turn_state_schedule"

// Turn-state 定时来源
const (
	TurnStateSourceBuiltin  = "builtin"  // 服务器直连上游
	TurnStateSourceExternal = "external" // 外部 py 服务（本地 Clash + 换节点）
)

// TurnStateScheduleConfig 是每账号的定时取件配置。
type TurnStateScheduleConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"interval_minutes"`
	Source          string `json:"source"`
}

// NormalizeTurnStateSource 归一化来源，非法值回退 builtin。
func NormalizeTurnStateSource(source string) string {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case TurnStateSourceExternal:
		return TurnStateSourceExternal
	default:
		return TurnStateSourceBuiltin
	}
}

// NormalizeTurnStateScheduleConfig 从 credentials 里的原始值解析定时配置。
// credentials 经 JSONB 往返后嵌套对象是 map[string]interface{}，数字是 float64。
func NormalizeTurnStateScheduleConfig(raw interface{}) TurnStateScheduleConfig {
	cfg := TurnStateScheduleConfig{Source: TurnStateSourceBuiltin}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return cfg
	}
	if v, ok := m["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	switch v := m["interval_minutes"].(type) {
	case float64:
		cfg.IntervalMinutes = int(v)
	case int:
		cfg.IntervalMinutes = v
	case int64:
		cfg.IntervalMinutes = int(v)
	}
	if cfg.IntervalMinutes < 0 {
		cfg.IntervalMinutes = 0
	}
	if s, ok := m["source"].(string); ok {
		cfg.Source = NormalizeTurnStateSource(s)
	}
	return cfg
}

// ToCredentialMap 把配置转成写入 credentials JSONB 的对象。
func (c TurnStateScheduleConfig) ToCredentialMap() map[string]interface{} {
	return map[string]interface{}{
		"enabled":          c.Enabled,
		"interval_minutes": c.IntervalMinutes,
		"source":           NormalizeTurnStateSource(c.Source),
	}
}

// EffectiveTurnStateSchedule 返回账号当前的定时配置（读运行时字段）。
func (a *Account) EffectiveTurnStateSchedule() TurnStateScheduleConfig {
	if a == nil {
		return TurnStateScheduleConfig{Source: TurnStateSourceBuiltin}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.TurnStateSchedule
}

// ApplyAccountTurnStateSchedule 把管理端改动的定时配置同步到运行时账号。
func (s *Store) ApplyAccountTurnStateSchedule(dbID int64, cfg TurnStateScheduleConfig) bool {
	acc := s.FindByID(dbID)
	if acc == nil {
		return false
	}
	cfg.Source = NormalizeTurnStateSource(cfg.Source)
	acc.mu.Lock()
	acc.TurnStateSchedule = cfg
	acc.mu.Unlock()
	return true
}
