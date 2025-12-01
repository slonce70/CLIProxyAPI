# Factory Droid Integration Analysis

## Overview

This document contains research and analysis for integrating Factory Droid as an API provider in CLIProxyAPI.

**Goal:** Use Droid not as an agent, but as an API to access models like Claude Sonnet 4.5, Opus 4.5, GPT-5 Codex, etc.

**Problem:** Factory Droid does NOT have a public REST API for chat completions. It only provides:
- Interactive CLI (`droid`)
- Headless CLI (`droid exec` with JSON output)

---

## Factory Droid Key Information

### Authentication
- API Key: `FACTORY_API_KEY=fk-...`
- Get key at: https://app.factory.ai/settings/api-keys

### Installation
```bash
# macOS/Linux
curl -fsSL https://app.factory.ai/cli | sh

# Windows (PowerShell)
irm https://app.factory.ai/cli.ps1 | iex
```

### Pricing (Pro Plan - $20/month)
- 10 million standard tokens + 10 million bonus
- Cached tokens = 1/10 of standard token
- Overage: $2.70 per million tokens

### Available Models

| Model | ID | Multiplier | Notes |
|-------|-----|-----------|-------|
| Droid Core | `glm-4.6` | 0.25× | Fastest, cheapest |
| Claude Haiku 4.5 | `claude-haiku-4-5-20251001` | 0.4× | Fast |
| GPT-5.1 | `gpt-5.1` | 0.5× | |
| GPT-5.1 Codex | `gpt-5.1-codex` | 0.5× | Coding optimized |
| Gemini 3 Pro | `gemini-3-pro-preview` | 0.8× | |
| Claude Sonnet 4.5 | `claude-sonnet-4-5-20250929` | 1.2× | Best balance |
| Claude Opus 4.5 | `claude-opus-4-5-20251101` | 1.2× | Most capable |
| Claude Opus 4.1 | `claude-opus-4-1-20250805` | 6× | Premium |

---

## Integration Approaches

### Approach A: Droid Exec Wrapper (Recommended)

Create a Go executor that spawns `droid exec` as subprocess and parses output.

**Command:**
```bash
droid exec -m <model> -o stream-json "<prompt>"
```

**Pros:**
- Uses official CLI
- Stable, follows official updates
- No reverse engineering needed

**Cons:**
- Subprocess overhead (~100-500ms)
- Not true HTTP streaming
- Process management complexity

### Approach B: Reverse Engineering Factory API

Intercept HTTP traffic from Droid CLI to find internal endpoints.

**Steps:**
1. Run `droid` with proxy (mitmproxy/charles)
2. Document API endpoints and format
3. Implement direct HTTP client

**Potential endpoints:**
- `api.factory.ai`
- Custom delegation URL (see `--delegation-url` flag)

**Pros:**
- True streaming
- Lower latency
- Direct HTTP integration

**Cons:**
- Unofficial, may break
- TOS considerations
- Maintenance burden

### Approach C: Not Viable

Using Factory's BYOK (Bring Your Own Key) is NOT applicable here because:
- BYOK is for using YOUR keys with Factory
- We want to use Factory's pooled access to models
- Factory uses its own billing/token system

---

## Implementation Plan

### Phase 1: Research
1. Capture `droid exec` traffic with proxy
2. Document API format
3. Test with different models

### Phase 2: Executor Implementation
```
internal/auth/droid/
├── executor.go        # ProviderExecutor interface
├── translator.go      # OpenAI ↔ Droid format
├── process.go         # Subprocess management
├── api_client.go      # Direct HTTP (if viable)
└── models.go          # Model definitions
```

### Phase 3: Integration
1. Add models to `registry/model_definitions.go`
2. Register translator
3. Add FACTORY_API_KEY auth flow

---

## Relevant Documentation Links

- [Droid Exec Overview](https://docs.factory.ai/cli/droid-exec/overview)
- [CLI Reference](https://docs.factory.ai/reference/cli-reference)
- [Pricing](https://docs.factory.ai/pricing)
- [BYOK Configuration](https://docs.factory.ai/cli/configuration/byok)
- [Building Apps with Droid Exec](https://docs.factory.ai/guides/building/droid-exec-tutorial)

---

## Implementation Status

### Completed
- [x] Research droid exec output formats
- [x] Create executor structure (`internal/auth/droid/`)
- [x] Implement ProviderExecutor interface
- [x] Model definitions and mapping
- [x] Integration documentation

### Pending (requires Go environment)
- [ ] Build verification
- [ ] Register executor in main server
- [ ] End-to-end testing
- [ ] Performance benchmarks

## Files in this directory

- `README.md` - This file (overview)
- `INTEGRATION.md` - Step-by-step integration guide
- `models.json` - Model definitions with pricing
- `droid-exec-format.md` - Output format documentation  
- `api-research.md` - API research notes
- `example-auth.json` - Example auth configuration

## Implementation Files

```
internal/auth/droid/
├── executor.go    # ProviderExecutor implementation
├── process.go     # Subprocess management
└── models.go      # Model definitions
```
