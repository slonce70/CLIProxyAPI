# Factory Droid API Research

## Status: IN PROGRESS

**Date:** 2024-12-01
**Droid CLI Version:** 0.27.4
**API Key Format:** `fk-...` (stored in .env, not committed)

---

## Test Results

### JSON Output Format

```bash
droid exec -o json "Say hello in one word"
```

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 5642,
  "num_turns": 1,
  "result": "Hello!",
  "session_id": "7719f871-23f8-41ac-b004-60dfa547ed54"
}
```

### Stream-JSON Output Format

```bash
droid exec -o stream-json "What is 2+2? Answer with just the number."
```

Events captured (in order):

**1. System Init Event:**
```json
{
  "type": "system",
  "subtype": "init",
  "cwd": "C:\\project\\CLIProxyAPI",
  "session_id": "d0202e04-3387-43af-989a-4d1ef5939dd9",
  "tools": ["Read", "LS", "Execute", "Edit", ...],
  "model": "claude-opus-4-5-20251101"
}
```

**2. User Message Event:**
```json
{
  "type": "message",
  "role": "user",
  "id": "17e4b798-3db5-4069-bab1-704c505dde0b",
  "text": "What is 2+2? Answer with just the number.",
  "timestamp": 1764591781067,
  "session_id": "d0202e04-3387-43af-989a-4d1ef5939dd9"
}
```

**3. Completion Event:**
```json
{
  "type": "completion",
  "finalText": "4",
  "numTurns": 1,
  "durationMs": 6172,
  "session_id": "d0202e04-3387-43af-989a-4d1ef5939dd9",
  "timestamp": 1764591787039
}
```

**4. Assistant Message Event:**
```json
{
  "type": "message",
  "role": "assistant",
  "id": "f73a164c-cafc-4c87-9964-6f3229faac8c",
  "text": "4",
  "timestamp": 1764591787028,
  "session_id": "d0202e04-3387-43af-989a-4d1ef5939dd9"
}
```

### Model Performance Comparison

| Model | Duration | Notes |
|-------|----------|-------|
| glm-4.6 | ~2.6s | Fastest, cheapest |
| claude-sonnet-4-5-20250929 | ~8.6s | Good balance |
| claude-opus-4-5-20251101 | ~6.2s | Default model |

### Default Model

When no `-m` flag specified, uses: `claude-opus-4-5-20251101`

---

## Research Tasks

### 1. Traffic Capture Setup

```bash
# Using mitmproxy
mitmproxy --mode regular --listen-port 8080

# Set proxy for droid (if supported)
export HTTPS_PROXY=http://localhost:8080
export HTTP_PROXY=http://localhost:8080

# Or check for proxy env vars
droid --help | grep -i proxy
```

### 2. Known Endpoints (from docs)

- `app.factory.ai` - Web application
- `api.factory.ai` - Likely API endpoint (to verify)
- Custom delegation URL via `--delegation-url` flag

### 3. CLI Flags for Investigation

```bash
# Delegation URL override
droid exec --delegation-url <url> "test"

# Debug output might reveal endpoints
FACTORY_DEBUG=1 droid exec "test"
```

---

## Captured Requests

### Request 1: [To be filled]

```http
POST /api/v1/??? HTTP/2
Host: ???.factory.ai
Authorization: Bearer fk-...
Content-Type: application/json

{
  // Request body
}
```

### Response 1: [To be filled]

```json
{
  // Response body
}
```

---

## API Format Analysis

### Authentication
- Header: `Authorization: Bearer fk-...`
- Or: Query param `api_key=fk-...`

### Request Format (Hypothesis)

Based on stream-json output, likely similar to:

```json
{
  "model": "claude-sonnet-4-5-20250929",
  "messages": [
    {"role": "user", "content": "..."}
  ],
  "stream": true,
  "tools": [...],
  "reasoning_effort": "medium"
}
```

### Response Format (Hypothesis)

Likely SSE or JSONL stream matching `stream-json` output format.

---

## Notes

- Droid CLI is built with Node.js/TypeScript (based on installation script)
- May use WebSocket for real-time features
- Session management suggests server-side state

---

## Next Steps

1. [ ] Install mitmproxy/charles
2. [ ] Capture authentication flow
3. [ ] Capture chat completion request
4. [ ] Document request/response format
5. [ ] Test direct API calls
6. [ ] Implement Go client
