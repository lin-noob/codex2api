package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// TurnStateGlobalSettings 是 turn-state 取件的全局配置（存 system_settings 单行）。
// 定时任务后台执行「外部接口」取件时需要这份 URL+token，前端 localStorage 拿不到，
// 所以落库到服务器端。
type TurnStateGlobalSettings struct {
	ExternalURL   string
	ExternalToken string
}

type TurnStateGlobalSettingsUpdate struct {
	ExternalURL   *string
	ExternalToken *string
}

func (db *DB) GetTurnStateGlobalSettings(ctx context.Context) (TurnStateGlobalSettings, error) {
	var value TurnStateGlobalSettings
	err := db.conn.QueryRowContext(ctx, `
		SELECT
			COALESCE(turn_state_external_url, ''),
			COALESCE(turn_state_external_token, '')
		FROM system_settings
		WHERE id = 1
	`).Scan(&value.ExternalURL, &value.ExternalToken)
	if err != nil {
		if err == sql.ErrNoRows {
			return TurnStateGlobalSettings{}, nil
		}
		return TurnStateGlobalSettings{}, fmt.Errorf("读取 turn-state 全局设置失败: %w", err)
	}
	value.ExternalURL = strings.TrimSpace(value.ExternalURL)
	value.ExternalToken = strings.TrimSpace(value.ExternalToken)
	return value, nil
}

func (db *DB) UpdateTurnStateGlobalSettings(ctx context.Context, update TurnStateGlobalSettingsUpdate) (TurnStateGlobalSettings, error) {
	current, err := db.GetTurnStateGlobalSettings(ctx)
	if err != nil {
		return TurnStateGlobalSettings{}, err
	}
	if update.ExternalURL != nil {
		current.ExternalURL = strings.TrimSpace(*update.ExternalURL)
	}
	if update.ExternalToken != nil {
		current.ExternalToken = strings.TrimSpace(*update.ExternalToken)
	}

	if _, err := db.conn.ExecContext(ctx, `INSERT INTO system_settings (id) VALUES (1) ON CONFLICT(id) DO NOTHING`); err != nil {
		return TurnStateGlobalSettings{}, fmt.Errorf("初始化 turn-state 全局设置失败: %w", err)
	}
	_, err = db.conn.ExecContext(ctx, `
		UPDATE system_settings SET
			turn_state_external_url = $1,
			turn_state_external_token = $2
		WHERE id = 1
	`, current.ExternalURL, current.ExternalToken)
	if err != nil {
		return TurnStateGlobalSettings{}, fmt.Errorf("保存 turn-state 全局设置失败: %w", err)
	}
	return current, nil
}
