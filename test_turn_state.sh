#!/bin/bash
TOKEN=$(python -c "import sys,json;print(json.load(sys.stdin)['tokens']['access_token'])" < ~/.codex/auth.json)
curl -si https://api.openai.com/v1/responses \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4.1-mini","input":"hi"}' \
  2>&1 | head -40
