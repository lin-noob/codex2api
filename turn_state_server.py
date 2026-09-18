"""本地 Turn-State 取件服务。

在跑 Clash 的机器上运行，对外暴露一个 HTTP 接口：
  POST /turn-state   body: {"token": "...", "account_id": "...", "model": "gpt-6-astra"}
                     header: X-Auth-Token: <API_AUTH_TOKEN>
  返回: {"turn_state": "...", "length": 292, "model": "...", "node": "...", "attempts": 2}

服务内部走本地 Clash 代理，并自动切换节点重试，直到拿到目标长度的 state。
外部服务器通过内网穿透（frp / cloudflared 等）调用本接口即可，无需直连 Clash。

依赖: pip install websocket-client python-socks
"""
import json
import logging
import threading
import time
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import websocket

# 日志：同时输出到控制台和文件 turn_state_server.log，带时间戳。
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
    handlers=[
        logging.StreamHandler(),
        logging.FileHandler("turn_state_server.log", encoding="utf-8"),
    ],
)
log = logging.getLogger("turn-state")

# ============================== 配置区 ==============================
LISTEN_HOST = "127.0.0.1"        # 监听地址；配合内网穿透用 127.0.0.1 即可
LISTEN_PORT = 8790               # 本接口监听端口
API_AUTH_TOKEN = "AdminTest."  # 调用方须在 header X-Auth-Token 带上，防止被人白嫖

CLASH_PROXY = "http://127.0.0.1:7890"   # 本地 Clash 代理端口
CLASH_API = "http://127.0.0.1:9097"     # 本地 Clash 控制 API
CLASH_SECRET = "mysecret123"            # Clash 控制 API secret，无则留空
CLASH_SELECTOR = "追云加速"             # 承载流量的策略组（规则模式下的主 Selector）

ALLOWED_MODELS = {"gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"}
TARGET_LENGTH = 292
# 实测规律：密集连发会让上游把限流元数据写进 state（blob 变长 → 312），
# 间隔单发才拿得到 292。所以重试要「少而慢」：次数少、每次间隔十几秒。
MAX_ATTEMPTS = 6
ATTEMPT_INTERVAL = 12  # 秒，两次尝试之间的等待
WS_HOST = "wss://chatgpt.com/backend-api/codex/responses"
# ==================================================================

# 换节点是 Clash 的全局状态，并发请求会互相干扰，用锁串行化。
_lock = threading.Lock()


def clash_api(method, path, body=None):
    req = urllib.request.Request(
        CLASH_API + path,
        method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={
            "Content-Type": "application/json",
            **({"Authorization": f"Bearer {CLASH_SECRET}"} if CLASH_SECRET else {}),
        },
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        raw = resp.read()
        return json.loads(raw) if raw else {}


def list_nodes():
    """返回按「区域轮询」排序的真实节点列表。

    state 长度与上游区域版本有关（灰度发布），同区域所有节点结果相同，
    所以逐个顺序试同区域节点是纯浪费。这里把节点按区域分桶后交错排列：
    每个区域先试 1 个，快速扫遍所有区域，大幅提高命中目标长度的概率。
    """
    all_proxies = clash_api("GET", "/proxies").get("proxies", {})
    selector = all_proxies.get(CLASH_SELECTOR, {})
    nodes = selector.get("all", [])

    skip = {"DIRECT", "REJECT", "PASS", "GLOBAL", CLASH_SELECTOR}
    # 嵌套策略组（Fallback/URLTest/Selector 等）不是真实节点，按类型过滤。
    group_types = {"Selector", "URLTest", "Fallback", "LoadBalance", "Relay", "Direct", "Reject"}
    junk_kw = ("流量", "到期", "官网", "剩余", "套餐", "http", "重置", "GB", "过期", "防失联", "无痕")
    # 香港节点访问不了 ChatGPT，直接跳过（覆盖常见命名：香港/HK/Hong Kong/🇭🇰）
    region_skip_kw = ("香港", "HK", "Hong Kong", "HongKong", "🇭🇰")

    real = []
    for n in nodes:
        if n in skip:
            continue
        info = all_proxies.get(n, {})
        if info.get("type") in group_types:
            continue
        if any(k in n for k in junk_kw) or any(k in n for k in region_skip_kw):
            continue
        real.append(n)

    # 按区域分桶：节点名去掉数字及其后缀作为区域键（"澳大利亚01【vip2】"→"澳大利亚"）。
    import re as _re
    buckets = {}
    order = []
    for n in real:
        region = _re.sub(r"\s*[0-9].*$", "", n).strip() or n
        if region not in buckets:
            buckets[region] = []
            order.append(region)
        buckets[region].append(n)

    # 交错排列：先每区域第 1 个，再每区域第 2 个……
    interleaved = []
    idx = 0
    while True:
        added = False
        for region in order:
            if idx < len(buckets[region]):
                interleaved.append(buckets[region][idx])
                added = True
        if not added:
            break
        idx += 1

    # 上次命中目标长度的节点排最前（区域版本短期内稳定，大概率仍有效）。
    global _last_good_node
    if _last_good_node and _last_good_node in interleaved:
        interleaved.remove(_last_good_node)
        interleaved.insert(0, _last_good_node)

    log.info("节点候选 %d 个，覆盖 %d 个区域: %s",
             len(interleaved), len(order), ", ".join(order))
    return interleaved


# 上次成功拿到目标长度的节点，下次优先尝试。
_last_good_node = None


def switch_node(node):
    clash_api(
        "PUT", f"/proxies/{urllib.parse.quote(CLASH_SELECTOR)}", {"name": node})


def build_proxy_kwargs(proxy_url):
    p = urllib.parse.urlparse(proxy_url)
    scheme = (p.scheme or "http").lower()
    proxy_type = "http" if scheme in ("http", "https") else scheme
    kwargs = {
        "http_proxy_host": p.hostname,
        "http_proxy_port": p.port,
        "proxy_type": proxy_type,
    }
    if p.username:
        kwargs["http_proxy_auth"] = (p.username, p.password or "")
    return kwargs


def fetch_once(token, account_id, model):
    """经本地 Clash 连一次上游，返回 (turn_state, 结束原因)。"""
    headers = {
        "Authorization": f"Bearer {token}",
        "OpenAI-Beta": "responses_websockets=2026-02-06",
        "Chatgpt-Account-Id": account_id,
        "Originator": "codex-tui",
        "User-Agent": "codex-tui/0.154.0 (Windows 10; x86_64) xterm-256color (codex-tui; 0.154.0)",
        "Version": "0.154.0",
        "X-Codex-Beta-Features": "remote_compaction_v2",
    }
    url = f"{WS_HOST}?model={model}"
    ws = websocket.create_connection(
        url,
        header=[f"{k}: {v}" for k, v in headers.items()],
        timeout=20,
        **build_proxy_kwargs(CLASH_PROXY),
    )
    try:
        ws.send(json.dumps({
            "type": "response.create",
            "model": model,
            "stream": True,
            "instructions": "",
            "input": [{"role": "user", "content": "hello"}],
        }))
        while True:
            msg = ws.recv()
            if not msg:
                return None, "closed"
            data = json.loads(msg)
            event_type = data.get("type", "")
            if event_type == "codex.response.metadata":
                ts = data.get("headers", {}).get("x-codex-turn-state", "")
                if ts:
                    return ts, "metadata"
            if event_type in ("response.completed", "response.failed", "error"):
                return None, event_type
    finally:
        ws.close()


def current_node():
    """读取策略组当前选中的节点。"""
    try:
        data = clash_api("GET", f"/proxies/{urllib.parse.quote(CLASH_SELECTOR)}")
        return data.get("now", "")
    except Exception:
        return ""


def generate(token, account_id, model):
    """间隔试探直到拿到目标长度的 state；失败抛异常。

    实测：密集连发会让上游把限流元数据编进 state（长度 312），间隔单发
    才能拿到 292。所以第 1 次直接用当前节点试（不切换），之后每次先等
    ATTEMPT_INTERVAL 秒再换下一个区域的节点重试。
    """
    req_start = time.time()
    with _lock:
        nodes = list_nodes()
        log.info("策略组 [%s] 可用节点 %d 个，开始取件（目标长度 %d，间隔 %ds）",
                 CLASH_SELECTOR, len(nodes), TARGET_LENGTH, ATTEMPT_INTERVAL)
        if not nodes:
            raise RuntimeError(f"策略组 {CLASH_SELECTOR} 下没有可用节点")
        length_stats = {}  # 记录各次尝试拿到的长度分布，便于判断是 token 问题还是长度不符
        # 第 1 次尝试用当前选中节点（不切换）——当前节点往往是人工验证过好用的。
        now_node = current_node()
        rotation = [n for n in nodes if n != now_node]
        for attempt in range(1, MAX_ATTEMPTS + 1):
            if attempt == 1 and now_node:
                node = now_node
            else:
                # 间隔等待后再换节点重试：等待是关键（换时间窗），换节点是辅助。
                log.info("  等待 %ds 后换节点重试…", ATTEMPT_INTERVAL)
                time.sleep(ATTEMPT_INTERVAL)
                node = rotation[(attempt - 2) % len(rotation)] if rotation else now_node
                try:
                    switch_node(node)
                except Exception as e:
                    log.warning("  [%d/%d] 切换节点 [%s] 失败: %s",
                                attempt, MAX_ATTEMPTS, node, e)
                    continue
            t0 = time.time()
            try:
                ts, reason = fetch_once(token, account_id, model)
            except Exception as e:
                msg = str(e)
                # token 被吊销/401 是账号凭据问题，换节点也没用，立即中止别浪费 30 次。
                if "token_revoked" in msg or "401" in msg or "Unauthorized" in msg:
                    log.error("  [%d/%d] token 无效/已吊销，中止换节点重试: %s",
                              attempt, MAX_ATTEMPTS, msg.split(" -+-+- ")[0])
                    raise RuntimeError("账号 token 无效或已吊销（token_revoked），请刷新账号凭据")
                log.warning("  [%d/%d] node=[%s] 连接失败(%.1fs): %s",
                            attempt, MAX_ATTEMPTS, node, time.time() - t0, msg)
                continue
            length = len(ts) if ts else 0
            length_stats[length] = length_stats.get(length, 0) + 1
            elapsed = time.time() - t0
            if ts and length == TARGET_LENGTH:
                global _last_good_node
                _last_good_node = node
                log.info("  [%d/%d] node=[%s] ✓ 命中 长度=%d 耗时%.1fs",
                         attempt, MAX_ATTEMPTS, node, length, elapsed)
                log.info("请求成功：model=%s node=[%s] 尝试%d次 总耗时%.1fs",
                         model, node, attempt, time.time() - req_start)
                return {
                    "turn_state": ts,
                    "length": length,
                    "model": model,
                    "node": node,
                    "attempts": attempt,
                }
            detail = f"长度={length}" if ts else f"未拿到state（{reason}）"
            log.info("  [%d/%d] node=[%s] %s 耗时%.1fs 换节点",
                     attempt, MAX_ATTEMPTS, node, detail, elapsed)
        # 全部失败：打印长度分布帮助定位
        stats_str = ", ".join(f"长度{k}×{v}" for k,
                              v in sorted(length_stats.items()))
        log.error("请求失败：%d 次尝试均未拿到长度 %d 的 state。长度分布: [%s] 总耗时%.1fs",
                  MAX_ATTEMPTS, TARGET_LENGTH, stats_str or "无", time.time() - req_start)
        raise RuntimeError(
            f"{MAX_ATTEMPTS} 次尝试未拿到长度 {TARGET_LENGTH} 的 state（长度分布: {stats_str or '全部失败'}）")


class Handler(BaseHTTPRequestHandler):
    def _json(self, code, obj):
        data = json.dumps(obj, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/health":
            self._json(200, {"ok": True})
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        client = self.client_address[0] if self.client_address else "?"
        if self.path.split("?")[0] != "/turn-state":
            self._json(404, {"error": "not found"})
            return
        if API_AUTH_TOKEN and self.headers.get("X-Auth-Token") != API_AUTH_TOKEN:
            log.warning("← %s 鉴权失败（X-Auth-Token 不匹配）", client)
            self._json(401, {"error": "unauthorized"})
            return
        try:
            n = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(n) or "{}")
        except Exception:
            log.warning("← %s 请求体解析失败", client)
            self._json(400, {"error": "invalid json body"})
            return
        token = (body.get("token") or "").strip()
        account_id = (body.get("account_id") or "").strip()
        model = (body.get("model") or "").strip()
        if not token or not account_id or not model:
            self._json(400, {"error": "token / account_id / model 均为必填"})
            return
        if model not in ALLOWED_MODELS:
            self._json(
                400, {"error": f"model 必须是 {sorted(ALLOWED_MODELS)} 之一"})
            return
        log.info("=" * 60)
        log.info("← 收到请求 from=%s model=%s account_id=%s… token长度=%d",
                 client, model, account_id[:8], len(token))
        try:
            result = generate(token, account_id, model)
            self._json(200, result)
        except Exception as e:
            self._json(500, {"error": str(e)})

    def log_message(self, *args):
        pass  # 关闭默认访问日志，用自己的 log


if __name__ == "__main__":
    server = ThreadingHTTPServer((LISTEN_HOST, LISTEN_PORT), Handler)
    log.info("Turn-State 服务已启动: http://%s:%d", LISTEN_HOST, LISTEN_PORT)
    log.info("  POST /turn-state  (header: X-Auth-Token)")
    log.info("  Clash 代理: %s | 策略组: %s | 目标长度: %d | 最大尝试: %d",
             CLASH_PROXY, CLASH_SELECTOR, TARGET_LENGTH, MAX_ATTEMPTS)
    log.info("  日志同时写入 turn_state_server.log")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log.info("已停止")
        server.shutdown()
