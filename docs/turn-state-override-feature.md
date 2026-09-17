# Turn State Override (State 修改) 功能实现文档

## 概述

在 codex2api 项目中新增了 **Turn State Override（State 修改）** 功能。当 Codex 官方账号配置了该字段后，所有经此账号发出的出站请求会使用配置值覆盖 `X-Codex-Turn-State` 头，而非使用客户端回带的值。

**适用范围**：仅 Codex 官方账号，不影响 OpenAI Responses 中转 / Grok / Antigravity / Claude 账号。

---

## 背景知识

### X-Codex-Turn-State 是什么

`X-Codex-Turn-State` 是 OpenAI 上游在响应中铸造的不透明回合状态 blob，客户端在同一会话的后续请求中回带此值以实现 turn 延续。项目中 `proxy/handler.go:255` 定义了常量：

```go
const codexTurnStateHeader = "X-Codex-Turn-State"
```

### 五条关键规则

1. **不同账号**的 state 无法复用
2. **同号不同模型**的 state 无法复用
3. **同号同模型**可以跨 IP 复用
4. State **有效期**约 1 小时
5. State **长度**固定为 292 字符

### 已有的 Guard 机制

`proxy/codex_turn_state.go` 中的 `guardCodexTurnStateEcho` 会剥离跨账号的 turn-state 回带。Override 在 guard 之后执行，所以 guard 逻辑无需修改——即使 guard 剥离了客户端的值，override 仍会写入配置值。

---

## 架构设计

### 设计模式

沿用项目已有的 **credential-key 模式**（与 `timezone`、`codex_fingerprint_mode` 一致）：
- 值存储在数据库 `credentials` JSONB 列的 `turn_state_override` 键中
- 运行时通过 `Account.TurnStateOverride` 字段访问
- 管理端修改后通过 `ApplyAccountTurnStateOverride` 即时同步到运行时

### 请求链路中的覆盖位置

**HTTP 路径** (`proxy/executor.go`):
```
applyCodexRequestHeaders → ApplyCodexSessionHeaders → [Turn State Override] → applyAccountCustomHeaders
```

**WS 路径** (`proxy/wsrelay/executor.go`):
```
prepareWebsocketHeaders → forward headers → ApplyCodexFingerprintHeaders → [Turn State Override] → custom headers
```

Override 在 guard 之后、custom headers 之前执行，确保：
- guard 的安全逻辑不受影响
- custom headers 仍然拥有最后覆盖权

---

## 改动文件清单

### 1. `auth/codex_turn_state_override.go`（新建）

核心模块，参照 `auth/codex_timezone.go` 模式：

```go
const CodexTurnStateOverrideCredentialKey = "turn_state_override"

func NormalizeTurnStateOverride(value string) string
func (a *Account) EffectiveCodexTurnStateOverride() string
func (s *Store) ApplyAccountTurnStateOverride(dbID int64, override string) bool
```

- `NormalizeTurnStateOverride`: trim 空白
- `EffectiveCodexTurnStateOverride`: RLock 读取，relay 类账号（中转/Grok/Antigravity/Claude）返回空
- `ApplyAccountTurnStateOverride`: 运行时即时更新

### 2. `auth/store.go`

- **line 179-181**: Account 结构体新增 `TurnStateOverride string` 字段（在 `Timezone` 之后）
- **line 5464**: `buildAccountFromRow` 中读取 credential：
  ```go
  turnStateOverride := NormalizeTurnStateOverride(row.GetCredential(CodexTurnStateOverrideCredentialKey))
  ```
- **line 5505**: Account 字面量中赋值：`TurnStateOverride: turnStateOverride,`

### 3. `proxy/executor.go`

- **line 1244-1246**: `applyCodexRequestHeaders` 中，在 `ApplyCodexSessionHeaders` 之后插入：
  ```go
  if override := account.EffectiveCodexTurnStateOverride(); override != "" {
      req.Header.Set(codexTurnStateHeader, override)
  }
  ```

### 4. `proxy/wsrelay/executor.go`

- **line 371-373**: `prepareWebsocketHeaders` 中，在 `ApplyCodexFingerprintHeaders` 之后插入：
  ```go
  if override := account.EffectiveCodexTurnStateOverride(); override != "" {
      headers.Set("X-Codex-Turn-State", override)
  }
  ```

### 5. `admin/handler.go`

六处修改：

| 位置 | 修改内容 |
|------|---------|
| line 1681 | `accountResponse` 结构体加 `TurnStateOverride string \`json:"turn_state_override,omitempty"\`` |
| line 2149 | `updateAccountSchedulerReq` 加 `TurnStateOverride json.RawMessage` |
| line 2174 | `accountSchedulerUpdate` 加 `TurnStateOverride database.OptionalString` |
| line 2281 | `parseAccountSchedulerUpdate` 解析：`parseOptionalStringField(req.TurnStateOverride, "turn_state_override", nil)` |
| line 2318 | 写入 credential：`credentialUpdates[auth.CodexTurnStateOverrideCredentialKey] = strings.TrimSpace(turnStateOverride.Value)` |
| line 2782-2783 | `applyAccountSchedulerRuntimeUpdate` 调用：`h.store.ApplyAccountTurnStateOverride(id, update.TurnStateOverride.Value)` |

### 6. `admin/account_response_builder.go`

- **line 136-139**: Codex-only guard 块中读取 turn_state_override credential
- **line 260**: 响应结构体中赋值：`TurnStateOverride: turnStateOverride,`

### 7. `frontend/src/types.ts`

- `AccountRow` 加 `turn_state_override?: string`（line 324）
- `UpdateAccountSchedulerRequest` 加 `turn_state_override?: string | null`（line 1454）

### 8. `frontend/src/pages/Accounts.tsx`

| 修改点 | 说明 |
|--------|------|
| state 声明 | `const [editTurnStateOverride, setEditTurnStateOverride] = useState("")` |
| `populateSchedulerEditor` | 初始化：`setEditTurnStateOverride(account.turn_state_override ?? "")` |
| `closeSchedulerEditor` | 重置：`setEditTurnStateOverride("")` |
| `handleSaveScheduler` payload | 仅 Codex 官方账号下发：`turn_state_override: editTurnStateOverride.trim() \|\| null` |
| 编辑 Modal UI | 在绑定时区卡片之后、自定义请求头之前，新增 Turn State Override 输入卡片，含 292 字符长度警告 |

### 9. `frontend/src/locales/zh.json`

```json
"turnStateOverrideTitle": "State 修改",
"turnStateOverrideHint": "主动覆盖出站请求中的 X-Codex-Turn-State 头...",
"turnStateOverridePlaceholder": "留空 = 不覆盖（透传客户端值）",
"turnStateOverrideLengthWarning": "State 长度应为 292 字符，当前 {length} 字符"
```

### 10. `frontend/src/locales/en.json`

```json
"turnStateOverrideTitle": "Turn State Override",
"turnStateOverrideHint": "Override the X-Codex-Turn-State header in outbound requests...",
"turnStateOverridePlaceholder": "Leave empty = no override (pass through client value)",
"turnStateOverrideLengthWarning": "State length should be 292 characters, currently {length}"
```

### 11. `proxy/codex_turn_state.go`（清理）

- 移除调试用的 `log.Printf("[TurnState] len=%d value=%s", len(token), token)`
- 移除未使用的 `"log"` import

### 12. 删除测试文件

- `test_turn_state.py`（已删除）
- `test_turn_state.sh`（已删除）

---

## 编译状态

- **Go 后端**：`go build ./...` 编译通过
- **前端**：`npx vite build` 构建通过

---

## 部署方式

### Docker 完整版（PostgreSQL + Redis）

```bash
cp .env.example .env
# 按需编辑 .env（端口、代理等）
docker compose -f docker-compose.local.yml up -d --build
```

### Docker 轻量版（SQLite）

```bash
cp .env.sqlite.example .env
docker compose -f docker-compose.sqlite.local.yml up -d --build
```

启动后访问 `http://localhost:8080/admin`，首次进入设置管理密钥。

---

## 使用方式

1. 进入管理后台 → 账号列表 → 点击 Codex 官方账号的编辑按钮
2. 在编辑弹窗的「网络与请求」分组中找到「State 修改」卡片
3. 粘贴 292 字符的 turn-state 值 → 保存
4. 此后该账号的所有出站请求（HTTP 和 WS）都会使用该值覆盖 `X-Codex-Turn-State` 头
5. 清空该字段 → 保存，即恢复为透传客户端值

---

## 注意事项

- 前端仅做长度警告（≠292 显示 amber 提示），后端不硬校验长度，因为格式可能演变
- 中转型账号（OpenAI Responses API / Grok / Antigravity / Claude）编辑界面不显示该字段
- turn-state 有效期约 1 小时，过期后需要更新
- 同账号不同模型的 state 不可共用，配置时需注意匹配
