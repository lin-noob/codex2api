package admin

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/codex2api/auth"
)

const (
	turnStateScheduleScanInterval = 60 * time.Second // 每分钟检查一次哪些账号到点
	turnStateScheduleConcurrency  = 2                // 有界并发（外部取件较慢，不宜太高）
)

// StartTurnStateSchedule 启动 turn-state 定时取件后台循环。每账号独立开关，
// 按各自 interval_minutes 到点后重取三个模型的 state。配置变更时 wake 立即重扫。
func (h *Handler) StartTurnStateSchedule(ctx context.Context) {
	if h == nil || h.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.turnStateScheduleStartOnce.Do(func() {
		if h.turnStateScheduleWake == nil {
			h.turnStateScheduleWake = make(chan struct{}, 1)
		}
		if h.turnStateLastRun == nil {
			h.turnStateLastRun = make(map[int64]time.Time)
		}
		h.turnStateScheduleWG.Add(1)
		go func() {
			defer h.turnStateScheduleWG.Done()
			ticker := time.NewTicker(turnStateScheduleScanInterval)
			defer ticker.Stop()
			firstScan := true
			for {
				if firstScan {
					firstScan = false
				} else {
					select {
					case <-ctx.Done():
						return
					case <-h.turnStateScheduleWake:
					case <-ticker.C:
					}
				}
			drainSignals:
				for {
					select {
					case <-h.turnStateScheduleWake:
					case <-ticker.C:
					default:
						break drainSignals
					}
				}
				if ctx.Err() != nil {
					return
				}
				h.runTurnStateScheduleScan(ctx)
			}
		}()
	})
}

// WaitTurnStateSchedule 等待后台循环退出（调用前先取消 Start 的 context）。
func (h *Handler) WaitTurnStateSchedule() {
	if h != nil {
		h.turnStateScheduleWG.Wait()
	}
}

func (h *Handler) triggerTurnStateScheduleScan() {
	if h == nil || h.turnStateScheduleWake == nil {
		return
	}
	select {
	case h.turnStateScheduleWake <- struct{}{}:
	default:
	}
}

func (h *Handler) runTurnStateScheduleScan(ctx context.Context) {
	if h == nil || h.store == nil {
		return
	}

	// 外部来源需要的全局配置，一轮扫描读一次。
	externalURL, externalToken := "", ""
	if h.db != nil {
		if cfg, err := h.db.GetTurnStateGlobalSettings(ctx); err == nil {
			externalURL, externalToken = cfg.ExternalURL, cfg.ExternalToken
		}
	}

	now := time.Now()
	accounts := h.store.Accounts()
	sem := make(chan struct{}, turnStateScheduleConcurrency)
	var wg sync.WaitGroup

	for _, account := range accounts {
		if account == nil || account.IsRelayStyle() {
			continue
		}
		schedule := account.EffectiveTurnStateSchedule()
		if !schedule.Enabled || schedule.IntervalMinutes <= 0 {
			continue
		}
		id := account.ID()
		h.turnStateLastRunMu.Lock()
		last := h.turnStateLastRun[id]
		due := last.IsZero() || now.Sub(last) >= time.Duration(schedule.IntervalMinutes)*time.Minute
		if due {
			h.turnStateLastRun[id] = now
		}
		h.turnStateLastRunMu.Unlock()
		if !due {
			continue
		}
		if account.GetAccessToken() == "" {
			continue
		}

		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(acc *auth.Account, src string) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, model := range auth.CodexTurnStateModels {
				if ctx.Err() != nil {
					return
				}
				if _, _, err := h.runTurnStateGeneration(ctx, acc, model, src, "", externalURL, externalToken); err != nil {
					log.Printf("[turn-state-schedule] 账号 %d 模型 %s 取件失败: %v", acc.ID(), model, err)
				}
			}
		}(account, schedule.Source)
	}
	wg.Wait()
}
