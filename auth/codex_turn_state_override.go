package auth

import (
	"encoding/json"
	"strings"
)

const CodexTurnStateOverrideCredentialKey = "turn_state_override"

var CodexTurnStateModels = []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"}

func NormalizeTurnStateOverrideMap(raw interface{}) map[string]string {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case map[string]string:
		out := make(map[string]string, len(v))
		for k, val := range v {
			k = strings.TrimSpace(k)
			val = strings.TrimSpace(val)
			if k != "" && val != "" {
				out[k] = val
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case map[string]interface{}:
		out := make(map[string]string, len(v))
		for k, val := range v {
			k = strings.TrimSpace(k)
			if k == "" || val == nil {
				continue
			}
			s, ok := val.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" {
				out[k] = s
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		// backward compat: try to parse JSON string
		v = strings.TrimSpace(v)
		if v == "" {
			return nil
		}
		var m map[string]string
		if json.Unmarshal([]byte(v), &m) == nil && len(m) > 0 {
			return NormalizeTurnStateOverrideMap(m)
		}
		return nil
	default:
		return nil
	}
}

// NormalizeTurnStateOverride kept for backward compat — trims a single string.
func NormalizeTurnStateOverride(value string) string {
	return strings.TrimSpace(value)
}

func (a *Account) EffectiveCodexTurnStateOverrideForModel(model string) string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isRelayStyleLocked() {
		return ""
	}
	model = strings.TrimSpace(model)
	if model == "" || a.TurnStateOverride == nil {
		return ""
	}
	return strings.TrimSpace(a.TurnStateOverride[model])
}

// EffectiveCodexTurnStateOverride returns first non-empty state found (backward compat).
func (a *Account) EffectiveCodexTurnStateOverride() string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isRelayStyleLocked() {
		return ""
	}
	for _, v := range a.TurnStateOverride {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func (s *Store) ApplyAccountTurnStateOverride(dbID int64, override map[string]string) bool {
	acc := s.FindByID(dbID)
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	acc.TurnStateOverride = override
	acc.mu.Unlock()
	return true
}

func (s *Store) ApplyAccountTurnStateOverrideForModel(dbID int64, model, state string) bool {
	acc := s.FindByID(dbID)
	if acc == nil {
		return false
	}
	model = strings.TrimSpace(model)
	state = strings.TrimSpace(state)
	if model == "" {
		return false
	}
	acc.mu.Lock()
	if acc.TurnStateOverride == nil {
		acc.TurnStateOverride = make(map[string]string)
	}
	if state == "" {
		delete(acc.TurnStateOverride, model)
	} else {
		acc.TurnStateOverride[model] = state
	}
	acc.mu.Unlock()
	return true
}
