package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// DroidExecutor wraps Factory Droid CLI to provide OpenAI-compatible API access.
type DroidExecutor struct {
	cfg       *config.Config
	droidPath string
	pool      *DroidProcessPool
	poolMu    sync.Mutex
}

func NewDroidExecutor(cfg *config.Config) *DroidExecutor {
	return &DroidExecutor{cfg: cfg, droidPath: "droid"}
}

// InitPool initializes the process pool for faster request handling
func (e *DroidExecutor) InitPool(apiKey, model string, size int) error {
	e.poolMu.Lock()
	defer e.poolMu.Unlock()

	if e.pool != nil {
		e.pool.Close()
	}

	cwd, _ := os.Getwd()
	e.pool = NewDroidProcessPool(size, apiKey, model, cwd)
	log.Infof("droid: initialized process pool with size %d", size)
	return nil
}

// ClosePool shuts down the process pool
func (e *DroidExecutor) ClosePool() {
	e.poolMu.Lock()
	defer e.poolMu.Unlock()

	if e.pool != nil {
		e.pool.Close()
		e.pool = nil
	}
}

// PoolStats returns pool statistics (total, busy, idle)
func (e *DroidExecutor) PoolStats() (total, busy, idle int) {
	e.poolMu.Lock()
	defer e.poolMu.Unlock()

	if e.pool != nil {
		return e.pool.Stats()
	}
	return 0, 0, 0
}

func (e *DroidExecutor) Identifier() string { return "droid" }

func (e *DroidExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey := getDroidAPIKey(auth)
	if apiKey == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: missing FACTORY_API_KEY")
	}

	prompt, model, reasoningEffort, err := extractDroidPromptAndModel(req.Payload, req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to extract prompt: %w", err)
	}

	// Try pool-based execution first (much faster)
	if result, err := e.executeWithPool(ctx, apiKey, model, prompt); err == nil {
		response := convertDroidToOpenAIResponse(result, model)
		payload, err := json.Marshal(response)
		if err != nil {
			return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to marshal response: %w", err)
		}
		return cliproxyexecutor.Response{Payload: payload}, nil
	} else {
		log.Debugf("droid: pool execution failed, falling back to subprocess: %v", err)
	}

	// Fallback to subprocess execution
	result, err := executeDroidJSON(ctx, e.droidPath, apiKey, model, reasoningEffort, prompt)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: execution failed: %w", err)
	}

	response := convertDroidToOpenAIResponse(result, model)
	payload, err := json.Marshal(response)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to marshal response: %w", err)
	}

	return cliproxyexecutor.Response{Payload: payload}, nil
}

// executeWithPool tries to execute using the process pool
func (e *DroidExecutor) executeWithPool(ctx context.Context, apiKey, model, prompt string) (*droidJSONResult, error) {
	e.poolMu.Lock()
	pool := e.pool

	// Lazy initialization of pool if enabled in config
	if pool == nil && e.cfg != nil && len(e.cfg.DroidKey) > 0 {
		droidCfg := e.cfg.DroidKey[0]
		if droidCfg.PoolEnabled {
			poolSize := droidCfg.PoolSize
			if poolSize <= 0 {
				poolSize = 3
			}
			cwd, _ := os.Getwd()
			pool = NewDroidProcessPool(poolSize, apiKey, model, cwd)
			e.pool = pool
			log.Infof("droid: lazy-initialized process pool with size %d", poolSize)
		}
	}
	e.poolMu.Unlock()

	if pool == nil {
		return nil, fmt.Errorf("pool not initialized or disabled")
	}

	worker, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire worker: %w", err)
	}

	startTime := time.Now()
	response, err := worker.SendMessage(ctx, prompt)
	duration := time.Since(startTime)

	if err != nil {
		pool.ReleaseWithError(worker, err)
		return nil, err
	}

	pool.Release(worker)

	return &droidJSONResult{
		Type:       "result",
		Subtype:    "success",
		IsError:    false,
		DurationMs: duration.Milliseconds(),
		NumTurns:   1,
		Result:     response,
		SessionID:  worker.sessionID,
	}, nil
}

func (e *DroidExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	apiKey := getDroidAPIKey(auth)
	if apiKey == "" {
		return nil, fmt.Errorf("droid: missing FACTORY_API_KEY")
	}

	prompt, model, reasoningEffort, err := extractDroidPromptAndModel(req.Payload, req.Model)
	if err != nil {
		return nil, fmt.Errorf("droid: failed to extract prompt: %w", err)
	}

	ch := make(chan cliproxyexecutor.StreamChunk, 100)

	go func() {
		defer close(ch)
		if err := executeDroidStreamJSON(ctx, e.droidPath, apiKey, model, reasoningEffort, prompt, ch); err != nil {
			ch <- cliproxyexecutor.StreamChunk{Err: err}
		}
	}()

	return ch, nil
}

func (e *DroidExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *DroidExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("droid: token counting not supported")
}

func getDroidAPIKey(auth *cliproxyauth.Auth) string {
	if auth != nil && auth.Attributes != nil {
		if key := auth.Attributes["api_key"]; key != "" {
			return key
		}
	}
	return os.Getenv("FACTORY_API_KEY")
}

type droidOpenAIRequest struct {
	Model    string               `json:"model"`
	Messages []droidOpenAIMessage `json:"messages"`
}

type droidOpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func extractDroidPromptAndModel(payload []byte, defaultModel string) (prompt string, model string, reasoningEffort string, err error) {
	var req droidOpenAIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return "", "", "", err
	}

	model = req.Model
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		model = "claude-opus-4-5-20251101"
	}
	model, reasoningEffort = mapDroidModel(model)

	// Build prompt preserving conversation context
	// Format that LLMs understand well for multi-turn conversations
	var promptBuilder strings.Builder
	var systemPrompt string
	var hasHistory bool

	// Extract system prompt first
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			break
		}
	}

	// Check if we have conversation history (more than just the last user message)
	userMsgCount := 0
	for _, msg := range req.Messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			userMsgCount++
		}
	}
	hasHistory = userMsgCount > 1

	// Add system prompt if present
	if systemPrompt != "" {
		promptBuilder.WriteString("<system>\n")
		promptBuilder.WriteString(systemPrompt)
		promptBuilder.WriteString("\n</system>\n\n")
	}

	// Add conversation history if present
	if hasHistory {
		promptBuilder.WriteString("<conversation_history>\n")
		for _, msg := range req.Messages {
			switch msg.Role {
			case "system":
				continue // Already handled
			case "user":
				promptBuilder.WriteString("[User]: ")
				promptBuilder.WriteString(msg.Content)
				promptBuilder.WriteString("\n\n")
			case "assistant":
				promptBuilder.WriteString("[Assistant]: ")
				promptBuilder.WriteString(msg.Content)
				promptBuilder.WriteString("\n\n")
			}
		}
		promptBuilder.WriteString("</conversation_history>\n\n")
		promptBuilder.WriteString("Continue the conversation. Respond to the last user message.")
	} else {
		// Single message - just use it directly
		for _, msg := range req.Messages {
			if msg.Role == "user" {
				promptBuilder.WriteString(msg.Content)
			}
		}
	}

	return strings.TrimSpace(promptBuilder.String()), model, reasoningEffort, nil
}

// mapDroidModel returns the native model name and reasoning effort level.
// Models with -thinking suffix get "high" reasoning, others get empty (use defaults).
func mapDroidModel(model string) (nativeModel string, reasoningEffort string) {
	// Check for thinking suffix first
	reasoningEffort = ""
	baseModel := model
	if strings.HasSuffix(model, "-thinking") {
		reasoningEffort = "high"
		baseModel = strings.TrimSuffix(model, "-thinking")
	}

	modelMap := map[string]string{
		"droid-glm-4.6":           "glm-4.6",
		"droid-claude-haiku-4.5":  "claude-haiku-4-5-20251001",
		"droid-claude-sonnet-4.5": "claude-sonnet-4-5-20250929",
		"droid-claude-opus-4.5":   "claude-opus-4-5-20251101",
		"droid-gpt-5.1-codex":     "gpt-5.1-codex",
		"droid-gemini-3-pro":      "gemini-3-pro-preview",
	}

	if native, ok := modelMap[baseModel]; ok {
		// GLM-4.6 doesn't support reasoning, clear it
		if native == "glm-4.6" {
			reasoningEffort = ""
		}
		return native, reasoningEffort
	}
	return model, reasoningEffort
}

type droidJSONResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	DurationMs int64  `json:"duration_ms"`
	NumTurns   int    `json:"num_turns"`
	Result     string `json:"result"`
	SessionID  string `json:"session_id"`
}

func executeDroidJSON(ctx context.Context, droidPath, apiKey, model, reasoningEffort, prompt string) (*droidJSONResult, error) {
	args := []string{"exec", "-o", "json"}
	if model != "" {
		args = append(args, "-m", model)
	}
	if reasoningEffort != "" {
		args = append(args, "-r", reasoningEffort)
	} else {
		// Disable reasoning by default for faster responses (except GLM which doesn't support it)
		if model != "glm-4.6" {
			args = append(args, "-r", "off")
		}
	}

	// Disable tools to reduce LLM prompt size and improve response time (~1 sec faster)
	// Tool names from: droid exec --list-tools
	args = append(args, "--disabled-tools", "Read,LS,Execute,Grep,Glob,WebSearch,FetchUrl,TodoWrite")

	// Pass prompt via stdin to avoid Windows command line length limits
	// Only add prompt as argument if it's short enough
	if len(prompt) < 4000 {
		args = append(args, prompt)
	}

	cmd := exec.CommandContext(ctx, droidPath, args...)
	cmd.Env = append(os.Environ(), "FACTORY_API_KEY="+apiKey)

	// For long prompts, pass via stdin
	if len(prompt) >= 4000 {
		cmd.Stdin = strings.NewReader(prompt)
	}

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("droid exec failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("droid exec failed: %w", err)
	}

	var result droidJSONResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse droid output: %w", err)
	}

	if result.IsError {
		return nil, newDroidError(result.Result)
	}

	return &result, nil
}

// droidError represents an error from the Droid CLI with HTTP status code mapping
type droidError struct {
	message    string
	statusCode int
}

func (e *droidError) Error() string {
	return e.message
}

func (e *droidError) StatusCode() int {
	return e.statusCode
}

// newDroidError creates a droidError with appropriate HTTP status code based on error message
func newDroidError(msg string) *droidError {
	lowerMsg := strings.ToLower(msg)

	// Balance/billing errors -> 402 Payment Required
	if strings.Contains(lowerMsg, "billing") ||
		strings.Contains(lowerMsg, "reload your tokens") ||
		strings.Contains(lowerMsg, "balance") ||
		strings.Contains(lowerMsg, "insufficient") {
		return &droidError{message: "droid error: " + msg, statusCode: 402}
	}

	// Rate limit errors -> 429 Too Many Requests
	if strings.Contains(lowerMsg, "rate limit") ||
		strings.Contains(lowerMsg, "too many requests") ||
		strings.Contains(lowerMsg, "quota") {
		return &droidError{message: "droid error: " + msg, statusCode: 429}
	}

	// Auth errors -> 401 Unauthorized
	if strings.Contains(lowerMsg, "unauthorized") ||
		strings.Contains(lowerMsg, "invalid api key") ||
		strings.Contains(lowerMsg, "authentication") {
		return &droidError{message: "droid error: " + msg, statusCode: 401}
	}

	// Default -> 500 Internal Server Error
	return &droidError{message: "droid error: " + msg, statusCode: 500}
}

type droidOpenAIResponse struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []droidOpenAIChoice `json:"choices"`
}

type droidOpenAIChoice struct {
	Index        int                `json:"index"`
	Message      droidOpenAIMessage `json:"message"`
	FinishReason string             `json:"finish_reason"`
}

func convertDroidToOpenAIResponse(result *droidJSONResult, model string) *droidOpenAIResponse {
	return &droidOpenAIResponse{
		ID:      "chatcmpl-droid-" + result.SessionID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []droidOpenAIChoice{
			{
				Index: 0,
				Message: droidOpenAIMessage{
					Role:    "assistant",
					Content: result.Result,
				},
				FinishReason: "stop",
			},
		},
	}
}

type droidStreamEvent struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Text      string `json:"text,omitempty"`
	FinalText string `json:"finalText,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
}

func executeDroidStreamJSON(ctx context.Context, droidPath, apiKey, model, reasoningEffort, prompt string, ch chan<- cliproxyexecutor.StreamChunk) error {
	args := []string{"exec", "-o", "stream-json"}
	if model != "" {
		args = append(args, "-m", model)
	}
	if reasoningEffort != "" {
		args = append(args, "-r", reasoningEffort)
	} else {
		// Disable reasoning by default for faster responses (except GLM which doesn't support it)
		if model != "glm-4.6" {
			args = append(args, "-r", "off")
		}
	}

	// Disable tools to reduce LLM prompt size and improve response time (~1 sec faster)
	// Tool names from: droid exec --list-tools
	args = append(args, "--disabled-tools", "Read,LS,Execute,Grep,Glob,WebSearch,FetchUrl,TodoWrite")

	// Pass prompt via stdin to avoid Windows command line length limits
	if len(prompt) < 4000 {
		args = append(args, prompt)
	}

	cmd := exec.CommandContext(ctx, droidPath, args...)
	cmd.Env = append(os.Environ(), "FACTORY_API_KEY="+apiKey)

	// For long prompts, pass via stdin
	if len(prompt) >= 4000 {
		cmd.Stdin = strings.NewReader(prompt)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start droid: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var sessionID string
	isFirst := true
	var lastText string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var event droidStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.WithError(err).Debug("droid: failed to parse stream event")
			continue
		}

		switch event.Type {
		case "system":
			sessionID = event.SessionID
			chunk := makeDroidStreamChunk("", model, sessionID, true, false)
			ch <- cliproxyexecutor.StreamChunk{Payload: chunk}
			isFirst = false

		case "message":
			if event.Role == "assistant" && event.Text != "" {
				newContent := event.Text
				if lastText != "" && strings.HasPrefix(newContent, lastText) {
					newContent = strings.TrimPrefix(newContent, lastText)
				}
				if newContent != "" {
					chunk := makeDroidStreamChunk(newContent, model, sessionID, isFirst, false)
					ch <- cliproxyexecutor.StreamChunk{Payload: chunk}
					isFirst = false
				}
				lastText = event.Text
			}

		case "completion":
			if event.FinalText != "" && event.FinalText != lastText {
				newContent := event.FinalText
				if lastText != "" && strings.HasPrefix(newContent, lastText) {
					newContent = strings.TrimPrefix(newContent, lastText)
				}
				if newContent != "" {
					chunk := makeDroidStreamChunk(newContent, model, sessionID, isFirst, false)
					ch <- cliproxyexecutor.StreamChunk{Payload: chunk}
				}
			}
			doneChunk := makeDroidStreamChunk("", model, sessionID, false, true)
			ch <- cliproxyexecutor.StreamChunk{Payload: doneChunk}
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading droid output: %w", err)
	}

	return cmd.Wait()
}

type droidStreamChunk struct {
	ID      string                   `json:"id"`
	Object  string                   `json:"object"`
	Created int64                    `json:"created"`
	Model   string                   `json:"model"`
	Choices []droidStreamChunkChoice `json:"choices"`
}

type droidStreamChunkChoice struct {
	Index        int                    `json:"index"`
	Delta        droidStreamChunkDelta  `json:"delta"`
	FinishReason *string                `json:"finish_reason"`
}

type droidStreamChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

func makeDroidStreamChunk(text, model, sessionID string, isFirst, isDone bool) []byte {
	chunk := droidStreamChunk{
		ID:      "chatcmpl-droid-" + sessionID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []droidStreamChunkChoice{
			{Index: 0, Delta: droidStreamChunkDelta{}},
		},
	}

	if isFirst {
		chunk.Choices[0].Delta.Role = "assistant"
	}

	if isDone {
		stop := "stop"
		chunk.Choices[0].FinishReason = &stop
	} else {
		chunk.Choices[0].Delta.Content = text
	}

	data, _ := json.Marshal(chunk)
	var buf bytes.Buffer
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}
