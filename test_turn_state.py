import json
import websocket

token = "eyJhbGciOiJSUzI1NiIsImtpZCI6Im4wejZQcjEtdEItMTdXb1U0VGM5OHp1RDBrNmx5YU1ZQmJ3SkFEOGtSVnMiLCJ0eXAiOiJKV1QifQ.eyJhdWQiOlsiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS92MSJdLCJjbGllbnRfaWQiOiJhcHBfRU1vYW1FRVo3M2YwQ2tYYVhwN2hyYW5uIiwiaHR0cHM6Ly9hcGkub3BlbmFpLmNvbS9hdXRoIjp7ImFtciI6WyJ1cm46b3BlbmFpOmFtcjpnb29nbGUiXSwiY2hhdGdwdF9hY2NvdW50X2lkIjoiMDA4MmExNzUtZTY4MS00ZjA5LWFhODYtZTExMThjZmQ4MmUwIiwiY2hhdGdwdF9hY2NvdW50X3VzZXJfaWQiOiJ1c2VyLVJFREoxSmZ6SHFMaUJVcWNzTDJhd1dGd19fMDA4MmExNzUtZTY4MS00ZjA5LWFhODYtZTExMThjZmQ4MmUwIiwiY2hhdGdwdF9jb21wdXRlX3Jlc2lkZW5jeSI6Im5vX2NvbnN0cmFpbnQiLCJjaGF0Z3B0X3BsYW5fdHlwZSI6InBybyIsImNoYXRncHRfdXNlcl9pZCI6InVzZXItUkVESjFKZnpIcUxpQlVxY3NMMmF3V0Z3IiwibG9jYWxob3N0Ijp0cnVlLCJwb2lkIjoib3JnLUtobjZHZDNvUzlwT0RoWDQxOHlBRDhqWCIsInVzZXJfaWQiOiJ1c2VyLVJFREoxSmZ6SHFMaUJVcWNzTDJhd1dGdyJ9LCJodHRwczovL2FwaS5vcGVuYWkuY29tL3Byb2ZpbGUiOnsiZW1haWwiOiJndXdlaTU5N0BnbWFpbC5jb20iLCJlbWFpbF92ZXJpZmllZCI6dHJ1ZSwibmFtZSI6Ilx1NGYxZiBcdTk4N2UifSwiaXNzIjoiaHR0cHM6Ly9hdXRoLm9wZW5haS5jb20iLCJwd2RfYXV0aF90aW1lIjoxNzg5MzkyMDg1MTg3LCJzY3AiOlsib3BlbmlkIiwicHJvZmlsZSIsImVtYWlsIiwib2ZmbGluZV9hY2Nlc3MiLCJhcGkuY29ubmVjdG9ycy5yZWFkIiwiYXBpLmNvbm5lY3RvcnMuaW52b2tlIl0sInNlc3Npb25faWQiOiJhdXRoc2Vzc19iNHVFbk1JZmJKMEpzR1ZocFExcm9YV0IiLCJzbCI6dHJ1ZSwic3ViIjoiZ29vZ2xlLW9hdXRoMnwxMDYwNDMxNzk3NDUxOTMzNzg4NzUiLCJpYXQiOjE3ODk2MzkzMTUsImV4cCI6MTc5MDUwMzMxNSwianRpIjoiMzY0MDkzMTAxMjVlNGUyMWJhYjU3ZDNkNzgyYTAzYzYiLCJuYmYiOjE3ODk2MzkzMTV9.Hx4N2TwTb1P17a9IdFfB6nG9buPJCsMJMpx_McYQ6Tp64Upc9ZmcYs68ejGA4EowP8yCvi0a7Be0ep9a7T96wOvIn1ar1B0CM5ZGqPzF_zh5efsfmi12Igts__QZtwTk5Qhm2K5afepTNR5-D-iGxgCgiNI_lig32ewne9nm3QL5Hv1IAMWAaSNfb1CEvKLYYsx0h-8elrziEKNBrBdl5U-sF9IaHFCjdYt6Tp0VkQMzHfLUS2NCC5gVj28n-zFnxJ4mloOXdeolFON09mYxSQf022xLgzoS_p4xQOsBN9labJZganYQfxjDP5mRxVzzwVoXDApK_9z8lFXJC48wOA"
account_id = "0082a175-e681-4f09-aa86-e1118cfd82e0"

url = "wss://chatgpt.com/backend-api/codex/responses?model=gpt-5.6-sol"
headers = {
    "Authorization": f"Bearer {token}",
    "OpenAI-Beta": "responses_websockets=2026-02-06",
    "Chatgpt-Account-Id": account_id,
    "Originator": "codex-tui",
    "User-Agent": "codex-tui/0.154.0 (Windows 10; x86_64) xterm-256color (codex-tui; 0.154.0)",
    "Version": "0.154.0",
    "X-Codex-Beta-Features": "remote_compaction_v2",
}

ws = websocket.create_connection(
    url, header=[f"{k}: {v}" for k, v in headers.items()])

ws.send(json.dumps({
    "type": "response.create",
    "model": "gpt-6-astra",
    "stream": True,
    "instructions": "",
    "input": [{"role": "user", "content": "hello"}],
}))

print("Waiting for messages...\n")
while True:
    msg = ws.recv()
    if not msg:
        break
    data = json.loads(msg)
    event_type = data.get("type", "")
    print(f"[{event_type}]")

    if event_type == "codex.response.metadata":
        hdrs = data.get("headers", {})
        turn_state = hdrs.get("x-codex-turn-state", "")
        print(f"\n{'='*50}")
        print(f"X-Codex-Turn-State")
        print(f"Length: {len(turn_state)}")
        print(f"Value:  {turn_state}")
        print(f"{'='*50}\n")

    if event_type in ("response.completed", "response.failed", "error"):
        print(json.dumps(data, indent=2, ensure_ascii=False))
        break

ws.close()
