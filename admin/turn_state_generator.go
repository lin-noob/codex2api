package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/security"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type generateTurnStateRequest struct {
	Model    string `json:"model"`
	ProxyURL string `json:"proxy_url"`
	// Source 取件来源：builtin（服务器直连）/ external（外部 py 服务）。
	// external 时外部接口地址/密钥优先用请求显式给的，否则读服务器端全局配置。
	Source        string `json:"source"`
	ExternalURL   string `json:"external_url"`
	ExternalToken string `json:"external_token"`
}

type generateTurnStateResponse struct {
	TurnState string `json:"turn_state"`
	Length    int    `json:"length"`
	Model     string `json:"model"`
}

// turnStateGenLog 是一条取件日志（内存缓冲，重启丢失）。
type turnStateGenLog struct {
	Time      string `json:"time"`
	AccountID int64  `json:"account_id"`
	Email     string `json:"email"`
	Model     string `json:"model"`
	Source    string `json:"source"`
	Node      string `json:"node,omitempty"`
	OK        bool   `json:"ok"`
	Length    int    `json:"length"`
	Error     string `json:"error,omitempty"`
}

const turnStateLogCap = 200

var (
	turnStateLogMu  sync.Mutex
	turnStateLogBuf []turnStateGenLog
)

func (h *Handler) appendTurnStateLog(entry turnStateGenLog) {
	if entry.Time == "" {
		entry.Time = time.Now().Format(time.RFC3339)
	}
	turnStateLogMu.Lock()
	turnStateLogBuf = append(turnStateLogBuf, entry)
	if len(turnStateLogBuf) > turnStateLogCap {
		turnStateLogBuf = turnStateLogBuf[len(turnStateLogBuf)-turnStateLogCap:]
	}
	turnStateLogMu.Unlock()
}

// snapshotTurnStateLogs 返回倒序（最新在前）的日志快照。
func snapshotTurnStateLogs() []turnStateGenLog {
	turnStateLogMu.Lock()
	defer turnStateLogMu.Unlock()
	out := make([]turnStateGenLog, 0, len(turnStateLogBuf))
	for i := len(turnStateLogBuf) - 1; i >= 0; i-- {
		out = append(out, turnStateLogBuf[i])
	}
	return out
}

func (h *Handler) GenerateTurnState(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的账号 ID"})
		return
	}

	var req generateTurnStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求体"})
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model 不能为空"})
		return
	}

	account := h.store.FindByID(id)
	if account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不在运行时池中"})
		return
	}
	if account.IsRelayStyle() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 Codex 官方账号"})
		return
	}

	if account.GetAccessToken() == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "账号缺少 access_token"})
		return
	}

	// 来源：优先看 source 字段；老前端只传 external_url 时也兼容识别为外部。
	externalURL := strings.TrimSpace(req.ExternalURL)
	externalToken := strings.TrimSpace(req.ExternalToken)
	source := auth.NormalizeTurnStateSource(req.Source)
	if req.Source == "" && externalURL != "" {
		source = auth.TurnStateSourceExternal
	}
	// 外部来源但请求没带 URL/token，回落到服务器端全局配置。
	if source == auth.TurnStateSourceExternal && externalURL == "" && h.db != nil {
		if cfg, cerr := h.db.GetTurnStateGlobalSettings(c.Request.Context()); cerr == nil {
			externalURL = cfg.ExternalURL
			externalToken = cfg.ExternalToken
		}
	}
	proxyURL := strings.TrimSpace(req.ProxyURL)

	turnState, _, err := h.runTurnStateGeneration(c.Request.Context(), account, req.Model, source, proxyURL, externalURL, externalToken)
	if err != nil {
		log.Printf("[TurnState] 生成失败 account=%d model=%s: %v", id, req.Model, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("生成失败: %v", err)})
		return
	}

	c.JSON(http.StatusOK, generateTurnStateResponse{
		TurnState: turnState,
		Length:    len(turnState),
		Model:     req.Model,
	})
}

// runTurnStateGeneration 是取件核心：按 source 走外部/内置，成功后写库+更新运行时，
// 并记一条日志。供 HTTP handler 和定时调度器共用。返回 (turnState, node, error)。
func (h *Handler) runTurnStateGeneration(parentCtx context.Context, account *auth.Account, model, source, proxyURL, externalURL, externalToken string) (string, string, error) {
	id := account.ID()
	accessToken := account.GetAccessToken()
	accountID := account.EffectiveAccountID()
	source = auth.NormalizeTurnStateSource(source)

	// 外部换节点取件可能重试多个节点，超时给足；内置 WS 一般很快。
	timeout := 30 * time.Second
	if source == auth.TurnStateSourceExternal {
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	var (
		turnState string
		node      string
		err       error
	)
	if source == auth.TurnStateSourceExternal {
		if externalURL == "" {
			err = fmt.Errorf("外部接口地址未配置")
		} else {
			turnState, node, err = fetchTurnStateViaExternalAPI(ctx, externalURL, externalToken, accessToken, accountID, model)
		}
	} else {
		var dialProxy string
		if dialProxy, err = h.resolveTurnStateDialProxy(account, proxyURL); err == nil {
			turnState, err = fetchTurnStateViaWebSocket(ctx, model, accessToken, accountID, dialProxy)
		}
	}

	if err != nil {
		h.appendTurnStateLog(turnStateGenLog{
			AccountID: id, Email: accountEmailForLog(account), Model: model,
			Source: source, OK: false, Error: err.Error(),
		})
		return "", "", err
	}

	h.store.ApplyAccountTurnStateOverrideForModel(id, model, turnState)
	credUpdate := map[string]interface{}{
		auth.CodexTurnStateOverrideCredentialKey: buildTurnStateCredentialMap(account, model, turnState),
	}
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if perr := h.db.UpdateCredentials(persistCtx, id, credUpdate); perr != nil {
		log.Printf("[TurnState] 持久化失败 account=%d model=%s: %v", id, model, perr)
	}
	persistCancel()

	h.appendTurnStateLog(turnStateGenLog{
		AccountID: id, Email: accountEmailForLog(account), Model: model,
		Source: source, Node: node, OK: true, Length: len(turnState),
	})
	return turnState, node, nil
}

// resolveTurnStateDialProxy 决定这次内置取件从哪个出口发出。
//
// 优先级：请求显式指定 > 代理池轮询 > 账号解析结果。
//
// 代理池开着时每次取件都从当前池里轮询取一条，而不是像业务转发那样按账号粘性绑定：
// 粘性的语义是「同账号恒定出口」，取件要的恰恰相反——每条连接换一个出口，好让上游
// 按源 IP 累计的限流元数据不会被编进 state（长度 312 的成因）。池里只有一条
// （例如本机的 IPv6 动态代理）时轮询退化成每次都用它，轮换由代理内部按连接完成。
func (h *Handler) resolveTurnStateDialProxy(account *auth.Account, requested string) (string, error) {
	if p := strings.TrimSpace(requested); p != "" {
		return p, nil
	}
	if next, enabled, _ := h.store.NextProxyPoolEntry(); enabled {
		// 池开着但被清空/全部测挂：显式失败，不回退到账号代理或直连——
		// 用户开池的意图就是"出口必须来自池"，静默改道比报错危险。
		if next == "" {
			return "", fmt.Errorf("代理池已启用但没有可用代理，已拒绝改走其他出口")
		}
		return next, nil
	}
	// 池关闭：沿用账号解析。fail-closed 判定该账号没有合法出口时返回空串，拿空串继续
	// 拨号会让请求以本机裸 IP 直发上游（issue #517），所以显式失败而不是静默直连。
	// usable 在「未启用代理池且无全局代理」时为 true、代理为空串——那是用户主动选择
	// 的直连，保持原有行为不动。
	resolved, usable := h.store.ResolveUsableProxyForAccount(account)
	if !usable {
		return "", fmt.Errorf("账号没有可用出口代理（代理池 fail-closed），已拒绝以本机 IP 直连上游")
	}
	return resolved, nil
}

// proxyURLPassword 取出代理 URL 里的密码，供擦除用。取不到返回空串。
func proxyURLPassword(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.User == nil {
		return ""
	}
	password, _ := parsed.User.Password()
	return password
}

// scrubProxySecret 把错误信息里的代理密码换成 ***。
//
// 池里的代理 URL 通常带密码。底层任何一层（net、x/net/proxy、gorilla）只要把代理 URL
// 拼进错误信息，密码就会顺着 log.Printf 落进日志、顺着取件日志进管理端。这里统一兜底。
// 没有发生泄漏时原样返回，保住 errors.Is/As 的包装链。
func scrubProxySecret(err error, proxyURL string) error {
	if err == nil {
		return nil
	}
	password := proxyURLPassword(proxyURL)
	if password == "" {
		return err
	}
	message := err.Error()
	scrubbed := strings.ReplaceAll(message, password, "***")
	if scrubbed == message {
		return err
	}
	return errors.New(scrubbed)
}

func accountEmailForLog(account *auth.Account) string {
	account.Mu().RLock()
	defer account.Mu().RUnlock()
	return account.Email
}

// fetchTurnStateViaExternalAPI 把取件委托给外部 py 服务（本地 Clash + 换节点）。
// baseURL 是外部服务根地址（如 cloudflare 隧道域名），自动追加 /turn-state。
// 返回 (turnState, node, error)，node 为外部服务命中的出口节点名（用于日志）。
func fetchTurnStateViaExternalAPI(ctx context.Context, baseURL, apiToken, token, accountID, model string) (string, string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/turn-state"
	payload, _ := json.Marshal(map[string]string{
		"token":      token,
		"account_id": accountID,
		"model":      model,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// ngrok 免费版会给疑似浏览器的请求插一个警告页，带上这个头直接跳过。
	req.Header.Set("ngrok-skip-browser-warning", "true")
	if apiToken != "" {
		req.Header.Set("X-Auth-Token", apiToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("调用外部接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("外部接口返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result struct {
		TurnState string `json:"turn_state"`
		Node      string `json:"node"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("解析外部接口响应失败: %w", err)
	}
	if strings.TrimSpace(result.TurnState) == "" {
		return "", "", fmt.Errorf("外部接口未返回 turn_state")
	}
	return result.TurnState, result.Node, nil
}

func buildTurnStateCredentialMap(account *auth.Account, model, state string) map[string]string {
	account.Mu().RLock()
	existing := account.TurnStateOverride
	account.Mu().RUnlock()

	result := make(map[string]string)
	for k, v := range existing {
		result[k] = v
	}
	if state == "" {
		delete(result, model)
	} else {
		result[model] = state
	}
	return result
}

// fetchTurnStateViaWebSocket 建一条全新 WS 连接取一次 turn-state。
//
// 「每次调用一条新连接、用完就关」这点是轮换出口的前提：ipv6-proxy 按出站 TCP 连接
// 随机绑源 IPv6，连接一旦建立就无法中途换地址。gorilla 的 Dialer 不做连接池，这里
// 也没有复用，所以每次取件都是一个新的出口 IPv6——不要给它加缓存或 keep-alive。
func fetchTurnStateViaWebSocket(ctx context.Context, model, accessToken, accountID, proxyURL string) (state string, err error) {
	// proxyURL 可能带密码（轮换出口的凭据来自密码文件）。底层任一层把它拼进错误
	// 信息都会顺着 log.Printf 落进日志、顺着取件日志进管理端，这里对所有返回路径
	// 统一兜底擦除。
	defer func() { err = scrubProxySecret(err, proxyURL) }()

	wsURL := fmt.Sprintf("wss://chatgpt.com/backend-api/codex/responses?model=%s", model)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)
	headers.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	if accountID != "" {
		headers.Set("Chatgpt-Account-Id", accountID)
	}
	headers.Set("Originator", "codex-tui")
	headers.Set("User-Agent", "codex-tui/0.154.0 (Windows 10; x86_64) xterm-256color (codex-tui; 0.154.0)")
	headers.Set("Version", "0.154.0")
	headers.Set("X-Codex-Beta-Features", "remote_compaction_v2")

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL != "" {
		parsed, perr := security.ParseProxyURL(proxyURL)
		if perr != nil {
			return "", fmt.Errorf("解析代理 URL 失败: %w", perr)
		}
		// socks5h 原样保留：x/net/proxy 的 FromURL 本来就认这个 scheme
		// （proxy.go: case "socks5", "socks5h"），而 gorilla 的代理拨号正是走它。
		// 且其 SOCKS5 实现对非 IP 字面量一律发 AddrTypeFQDN，DNS 本就在代理侧解析。
		// 之前降级成 socks5 是多余的，反而丢掉了 socks5h 的显式语义。
		dialer.Proxy = http.ProxyURL(parsed)
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return "", fmt.Errorf("WebSocket 连接失败: %w", err)
	}
	defer conn.Close()

	payload := map[string]interface{}{
		"type":         "response.create",
		"model":        model,
		"stream":       true,
		"instructions": "",
		"input":        []map[string]string{{"role": "user", "content": "hi"}},
	}
	payloadBytes, _ := json.Marshal(payload)
	if err := conn.WriteMessage(websocket.TextMessage, payloadBytes); err != nil {
		return "", fmt.Errorf("发送消息失败: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("超时")
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			return "", fmt.Errorf("读取消息失败: %w", err)
		}

		var msg struct {
			Type    string            `json:"type"`
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		if msg.Type == "codex.response.metadata" && msg.Headers != nil {
			if ts := strings.TrimSpace(msg.Headers["x-codex-turn-state"]); ts != "" {
				return ts, nil
			}
		}

		if msg.Type == "error" || msg.Type == "response.failed" {
			return "", fmt.Errorf("上游返回错误: %s", string(message))
		}

		if msg.Type == "response.completed" {
			return "", fmt.Errorf("未收到 turn-state（response.completed 无 metadata）")
		}
	}
}

type turnStateAccountInfo struct {
	ID         int64                    `json:"id"`
	Email      string                   `json:"email"`
	PlanType   string                   `json:"plan_type"`
	Disabled   bool                     `json:"disabled"`
	ProxyURL   string                   `json:"proxy_url"`
	TurnStates map[string]*turnStateVal `json:"turn_states"`
	Schedule   turnStateScheduleDTO     `json:"schedule"`
}

type turnStateVal struct {
	Value  string `json:"value"`
	Length int    `json:"length"`
}

type turnStateScheduleDTO struct {
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"interval_minutes"`
	Source          string `json:"source"`
}

func (h *Handler) ListTurnStates(c *gin.Context) {
	accounts := h.store.Accounts()
	result := make([]turnStateAccountInfo, 0)

	for _, acc := range accounts {
		if acc == nil || acc.IsRelayStyle() {
			continue
		}

		acc.Mu().RLock()
		email := acc.Email
		planType := acc.PlanType
		proxyURL := acc.ProxyURL
		stateMap := acc.TurnStateOverride
		schedule := acc.TurnStateSchedule
		acc.Mu().RUnlock()
		disabled := atomic.LoadInt32(&acc.Disabled) != 0

		turnStates := make(map[string]*turnStateVal, len(auth.CodexTurnStateModels))
		for _, model := range auth.CodexTurnStateModels {
			v := ""
			if stateMap != nil {
				v = stateMap[model]
			}
			turnStates[model] = &turnStateVal{Value: v, Length: len(v)}
		}

		// 只回账号自身绑定的代理作参考，不回解析结果：取件走的是代理池轮询，
		// 把解析出的粘性代理回给前端再被原样传回来，会以"显式指定"覆盖掉轮询。
		result = append(result, turnStateAccountInfo{
			ID:         acc.ID(),
			Email:      email,
			PlanType:   planType,
			Disabled:   disabled,
			ProxyURL:   proxyURL,
			TurnStates: turnStates,
			Schedule: turnStateScheduleDTO{
				Enabled:         schedule.Enabled,
				IntervalMinutes: schedule.IntervalMinutes,
				Source:          auth.NormalizeTurnStateSource(schedule.Source),
			},
		})
	}

	// 全局外部接口配置（token 只回是否已设置，不回明文）。
	externalConfig := gin.H{"url": "", "token_set": false}
	if h.db != nil {
		if cfg, err := h.db.GetTurnStateGlobalSettings(c.Request.Context()); err == nil {
			externalConfig = gin.H{"url": cfg.ExternalURL, "token_set": cfg.ExternalToken != ""}
		}
	}

	// 代理池状态：让页面说明当前取件出口来自哪里（池轮询 / 账号代理）。
	_, poolEnabled, poolSize := h.store.NextProxyPoolEntry()
	proxyPool := gin.H{"enabled": poolEnabled, "size": poolSize}

	c.JSON(http.StatusOK, gin.H{"accounts": result, "external_config": externalConfig, "proxy_pool": proxyPool})
}

type saveTurnStateScheduleRequest struct {
	Enabled         *bool   `json:"enabled"`
	IntervalMinutes *int    `json:"interval_minutes"`
	Source          *string `json:"source"`
}

func (h *Handler) SaveTurnStateSchedule(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的账号 ID"})
		return
	}

	var req saveTurnStateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求体"})
		return
	}

	account := h.store.FindByID(id)
	if account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不在运行时池中"})
		return
	}

	// 以当前配置为基线做增量更新。
	cfg := account.EffectiveTurnStateSchedule()
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.IntervalMinutes != nil {
		cfg.IntervalMinutes = *req.IntervalMinutes
	}
	if req.Source != nil {
		cfg.Source = auth.NormalizeTurnStateSource(*req.Source)
	}
	if cfg.IntervalMinutes < 0 {
		cfg.IntervalMinutes = 0
	}
	cfg.Source = auth.NormalizeTurnStateSource(cfg.Source)

	h.store.ApplyAccountTurnStateSchedule(id, cfg)
	credUpdate := map[string]interface{}{
		auth.CodexTurnStateScheduleCredentialKey: cfg.ToCredentialMap(),
	}
	if err := h.db.UpdateCredentials(c.Request.Context(), id, credUpdate); err != nil {
		log.Printf("[TurnState] 定时配置持久化失败 account=%d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	// 配置变更后唤醒调度器，开启即尽快执行一次。
	h.triggerTurnStateScheduleScan()

	c.JSON(http.StatusOK, gin.H{"success": true, "schedule": turnStateScheduleDTO{
		Enabled: cfg.Enabled, IntervalMinutes: cfg.IntervalMinutes, Source: cfg.Source,
	}})
}

type saveTurnStateConfigRequest struct {
	ExternalURL   *string `json:"external_url"`
	ExternalToken *string `json:"external_token"`
}

func (h *Handler) SaveTurnStateConfig(c *gin.Context) {
	var req saveTurnStateConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求体"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "数据库不可用"})
		return
	}
	cfg, err := h.db.UpdateTurnStateGlobalSettings(c.Request.Context(), database.TurnStateGlobalSettingsUpdate{
		ExternalURL:   req.ExternalURL,
		ExternalToken: req.ExternalToken,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "url": cfg.ExternalURL, "token_set": cfg.ExternalToken != ""})
}

func (h *Handler) GetTurnStateLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"logs": snapshotTurnStateLogs()})
}

type saveTurnStateRequest struct {
	TurnStates map[string]string `json:"turn_states"`
}

func (h *Handler) SaveTurnState(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的账号 ID"})
		return
	}

	var req saveTurnStateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求体"})
		return
	}

	account := h.store.FindByID(id)
	if account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不在运行时池中"})
		return
	}

	cleaned := make(map[string]string)
	for k, v := range req.TurnStates {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			cleaned[k] = v
		}
	}

	h.store.ApplyAccountTurnStateOverride(id, cleaned)

	credUpdate := map[string]interface{}{
		auth.CodexTurnStateOverrideCredentialKey: cleaned,
	}
	if err := h.db.UpdateCredentials(c.Request.Context(), id, credUpdate); err != nil {
		log.Printf("[TurnState] 持久化失败 account=%d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}
