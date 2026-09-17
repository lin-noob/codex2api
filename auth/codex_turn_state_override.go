package auth

import "strings"

// CodexTurnStateOverrideCredentialKey 是账号 turn-state 覆盖值在 credentials 中的存储键。
// 非空时，出站请求的 X-Codex-Turn-State 头将使用此值替换客户端回带的值。
const CodexTurnStateOverrideCredentialKey = "turn_state_override"

func NormalizeTurnStateOverride(value string) string {
	return strings.TrimSpace(value)
}

// EffectiveCodexTurnStateOverride 返回 Codex 官方出站路径生效的 turn-state 覆盖值。
// 中转型账号（OpenAI Responses / Grok / Antigravity / Claude）不走 Codex 官方出站，恒为空。
func (a *Account) EffectiveCodexTurnStateOverride() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isRelayStyleLocked() {
		return ""
	}
	return strings.TrimSpace(a.TurnStateOverride)
}

// ApplyAccountTurnStateOverride 把管理端改动的 turn-state 覆盖值同步到运行时账号。
func (s *Store) ApplyAccountTurnStateOverride(dbID int64, override string) bool {
	acc := s.FindByID(dbID)
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	acc.TurnStateOverride = NormalizeTurnStateOverride(override)
	acc.mu.Unlock()
	return true
}
