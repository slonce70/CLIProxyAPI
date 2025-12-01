# Droid Integration Guide

## Prerequisites

1. **Go 1.21+** installed
2. **Droid CLI** installed (`droid --version` should work)
3. **Factory API Key** (set as `FACTORY_API_KEY` environment variable)

## Files Created

```
internal/auth/droid/
├── executor.go    # ProviderExecutor implementation
├── process.go     # Subprocess management for droid exec
└── models.go      # Model definitions and mapping
```

## Integration Steps

### Step 1: Register Executor

In your main server initialization (e.g., `cmd/server/main.go` or where you create the auth manager):

```go
import (
    "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/droid"
    "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// Create manager
manager := auth.NewManager(store, selector, hook)

// Register Droid executor
droidExecutor := droid.NewExecutor("")  // Empty string uses "droid" from PATH
manager.RegisterExecutor(droidExecutor)
```

### Step 2: Register Models

In `internal/registry/model_definitions.go`, add:

```go
import "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/droid"

// In GetAllModels() or similar function:
func GetDroidModels() []*ModelInfo {
    return droid.GetDroidModels()
}
```

### Step 3: Add Auth Entry

Create a file `auths/droid.json`:

```json
{
  "id": "droid-default",
  "provider": "droid",
  "label": "Factory Droid",
  "attributes": {
    "api_key": "YOUR_FACTORY_API_KEY"
  }
}
```

Or use environment variable (recommended):
```json
{
  "id": "droid-default",
  "provider": "droid",
  "label": "Factory Droid"
}
```

And set `FACTORY_API_KEY` in your environment.

### Step 4: Configure Routing

In `config.yaml`, add Droid provider routing if needed:

```yaml
providers:
  droid:
    enabled: true
    default_model: droid-claude-opus-4.5
```

## Usage

### API Request

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "droid-claude-opus-4.5",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### Available Models

| Proxy Model ID | Native Droid Model |
|----------------|-------------------|
| `droid-glm-4.6` | `glm-4.6` |
| `droid-claude-haiku-4.5` | `claude-haiku-4-5-20251001` |
| `droid-claude-sonnet-4.5` | `claude-sonnet-4-5-20250929` |
| `droid-claude-opus-4.5` | `claude-opus-4-5-20251101` |
| `droid-gpt-5.1-codex` | `gpt-5.1-codex` |
| `droid-gemini-3-pro` | `gemini-3-pro-preview` |

You can also use native Droid model IDs directly.

## Limitations

1. **Token counting not supported** - Factory Droid doesn't expose token counting API
2. **Subprocess overhead** - Each request spawns a new `droid exec` process (~100-500ms overhead)
3. **No streaming optimization** - Streaming works but is simulated through subprocess stdout
4. **Tools/Functions** - Droid has its own tool system, not directly mapped to OpenAI functions

## Troubleshooting

### "droid: missing FACTORY_API_KEY"

Set the API key either in:
- Environment: `export FACTORY_API_KEY=fk-...`
- Auth file attributes: `"api_key": "fk-..."`

### "droid exec failed"

1. Check `droid --version` works
2. Verify API key is valid: `droid exec "test"`
3. Check model ID is correct

### Slow responses

This is expected due to subprocess overhead. For production use, consider:
1. Using connection pooling if implemented
2. Reverse engineering the internal API (see `api-research.md`)

## Testing

```bash
# Build
go build ./internal/auth/droid/...

# Test manually
FACTORY_API_KEY=fk-... droid exec -m glm-4.6 -o json "Say hello"

# Run integration tests (if available)
go test ./internal/auth/droid/...
```
