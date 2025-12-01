# Droid Exec Output Formats

## Command Syntax

```bash
droid exec [options] [prompt]

# From stdin
echo "prompt" | droid exec

# From file
droid exec -f prompt.md

# With options
droid exec -m claude-sonnet-4-5-20250929 -r medium -o stream-json "your prompt"
```

## Output Formats

### 1. Text (default)

Human-readable output:

```bash
$ droid exec "explain hello world in python"
The classic "Hello World" program in Python is simply...
```

### 2. JSON

Structured final output:

```bash
$ droid exec -o json "summarize this repo"
```

```json
{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "duration_ms": 5657,
  "num_turns": 1,
  "result": "This is a Factory documentation repository...",
  "session_id": "8af22e0a-d222-42c6-8c7e-7a059e391b0b"
}
```

### 3. Stream-JSON (for real-time processing)

JSONL format with event types:

```bash
$ droid exec -o stream-json "run ls command"
```

```jsonl
{"type":"system","subtype":"init","cwd":"/path/to/dir","session_id":"abc-123","tools":["Read","Execute"],"model":"claude-sonnet-4-5-20250929"}
{"type":"message","role":"user","id":"msg-1","text":"run ls command","timestamp":1762517060816,"session_id":"abc-123"}
{"type":"message","role":"assistant","id":"msg-2","text":"I'll run the ls command...","timestamp":1762517062000,"session_id":"abc-123"}
{"type":"tool_call","id":"call-1","messageId":"msg-2","toolId":"Execute","toolName":"Execute","parameters":{"command":"ls -la"},"timestamp":1762517062500,"session_id":"abc-123"}
{"type":"tool_result","id":"call-1","messageId":"msg-3","toolId":"Execute","isError":false,"value":"total 16\ndrwxr-xr-x...","timestamp":1762517063000,"session_id":"abc-123"}
{"type":"completion","finalText":"The ls command has been executed...","numTurns":1,"durationMs":3000,"session_id":"abc-123","timestamp":1762517064000}
```

## Event Types

| Type | Description | Key Fields |
|------|-------------|------------|
| `system` | Session initialization | `cwd`, `session_id`, `tools`, `model` |
| `message` | User or assistant text | `role`, `text`, `id`, `timestamp` |
| `tool_call` | Agent calling a tool | `toolName`, `parameters`, `id` |
| `tool_result` | Result from tool | `value`, `isError`, `id` |
| `completion` | Final event | `finalText`, `numTurns`, `durationMs` |

## Completion Event (Always Last)

The `completion` event is always emitted last and contains:

```json
{
  "type": "completion",
  "finalText": "The agent's final response text",
  "numTurns": 1,
  "durationMs": 3000,
  "session_id": "uuid"
}
```

## Parsing Examples

### Shell (jq)

```bash
# Extract final result
droid exec "analyze code" -o stream-json | \
  jq -r 'select(.type == "completion") | .finalText'

# Monitor tool calls
droid exec "complex task" -o stream-json | \
  jq -r 'select(.type == "tool_call") | "\(.toolName): \(.parameters)"'
```

### Go

```go
scanner := bufio.NewScanner(stdout)
for scanner.Scan() {
    var event map[string]interface{}
    json.Unmarshal(scanner.Bytes(), &event)
    
    switch event["type"] {
    case "message":
        if event["role"] == "assistant" {
            fmt.Println(event["text"])
        }
    case "completion":
        return event["finalText"].(string), nil
    }
}
```

### Node.js

```javascript
const proc = spawn('droid', ['exec', '-o', 'stream-json', prompt]);
let buffer = '';

proc.stdout.on('data', (chunk) => {
    buffer += chunk.toString();
    const lines = buffer.split('\n');
    buffer = lines.pop();
    
    for (const line of lines) {
        if (!line.trim()) continue;
        const event = JSON.parse(line);
        
        if (event.type === 'completion') {
            console.log('Result:', event.finalText);
        }
    }
});
```

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General runtime error |
| `2` | Invalid CLI arguments |
| Non-zero | Permission violation, tool error |

## Autonomy Levels

| Level | Flag | Allows |
|-------|------|--------|
| Default | (none) | Read-only: file reads, git diff, ls |
| Low | `--auto low` | File creation/editing |
| Medium | `--auto medium` | npm install, git commit, builds |
| High | `--auto high` | git push, deployments |
| Unsafe | `--skip-permissions-unsafe` | Everything (sandboxed only!) |

## Session Continuation

```bash
# Start session
RESULT=$(droid exec -o json "start task")
SESSION_ID=$(echo $RESULT | jq -r '.session_id')

# Continue session
droid exec -s $SESSION_ID "continue with next step"
```
