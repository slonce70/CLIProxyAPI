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

// droidDisabledTools contains all tools to disable for chat-only mode.
// We disable everything to use droid as a simple text completion API.
// Format: comma-separated list per --disabled-tools flag.
const droidDisabledTools = "" +
	// Core droid tools
	"Read,LS,Execute,Grep,Glob,Create,Edit,WebSearch,FetchUrl,TodoWrite," +
	"ExitSpecMode,GenerateDroid,store_agent_readiness_report,slack_post_message," +
	// MCP: brave-search (web searches)
	"brave-search___brave_web_search,brave-search___brave_local_search," +
	"brave-search___brave_news_search,brave-search___brave_video_search," +
	"brave-search___brave_image_search,brave-search___brave_summarizer," +
	// MCP: chrome-devtools (browser automation - MUST disable to prevent browser opening)
	"chrome-devtools___click,chrome-devtools___close_page,chrome-devtools___drag," +
	"chrome-devtools___emulate,chrome-devtools___evaluate_script,chrome-devtools___fill," +
	"chrome-devtools___fill_form,chrome-devtools___get_console_message," +
	"chrome-devtools___get_network_request,chrome-devtools___handle_dialog," +
	"chrome-devtools___hover,chrome-devtools___list_console_messages," +
	"chrome-devtools___list_network_requests,chrome-devtools___list_pages," +
	"chrome-devtools___navigate_page,chrome-devtools___new_page," +
	"chrome-devtools___performance_analyze_insight,chrome-devtools___performance_start_trace," +
	"chrome-devtools___performance_stop_trace,chrome-devtools___press_key," +
	"chrome-devtools___resize_page,chrome-devtools___select_page," +
	"chrome-devtools___take_screenshot,chrome-devtools___take_snapshot," +
	"chrome-devtools___upload_file,chrome-devtools___wait_for," +
	// MCP: context7 (documentation lookup)
	"context7___get-library-docs,context7___resolve-library-id"

// DroidExecutor wraps Factory Droid CLI to provide OpenAI-compatible API access.
type DroidExecutor struct {
	cfg            *config.Config
	droidPath      string
	pool           *DroidProcessPool
	poolMu         sync.Mutex
	sessionManager *DroidSessionManager
}

func NewDroidExecutor(cfg *config.Config) *DroidExecutor {
	var sessionTTL time.Duration = 30 * time.Minute
	if cfg != nil && len(cfg.DroidKey) > 0 && cfg.DroidKey[0].SessionTTL > 0 {
		sessionTTL = time.Duration(cfg.DroidKey[0].SessionTTL) * time.Minute
	}
	return &DroidExecutor{
		cfg:            cfg,
		droidPath:      "droid",
		sessionManager: NewDroidSessionManager(sessionTTL),
	}
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

	// Parse conversation
	conv, err := extractDroidConversation(req.Payload, req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to extract conversation: %w", err)
	}

	// Try session-based execution first (no token duplication)
	if result, err := e.executeWithSession(ctx, apiKey, conv); err == nil {
		response := convertDroidToOpenAIResponse(result, conv.Model)
		payload, err := json.Marshal(response)
		if err != nil {
			return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to marshal response: %w", err)
		}
		return cliproxyexecutor.Response{Payload: payload}, nil
	} else {
		log.Debugf("droid: session execution failed, falling back to subprocess: %v", err)
	}

	// Log request details for debugging token usage
	promptLen := len(conv.FullPrompt)
	log.Infof("droid: request model=%s reasoning=%s prompt_chars=%d (~%d tokens)", conv.Model, conv.ReasoningEffort, promptLen, promptLen/4)

	// Fallback to subprocess execution (sends full conversation each time)
	result, err := executeDroidJSON(ctx, e.droidPath, apiKey, conv.Model, conv.ReasoningEffort, conv.FullPrompt)
	if err != nil {
		log.Warnf("droid: execution failed: %v", err)
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: execution failed: %w", err)
	}

	// Log response details
	resultLen := len(result.Result)
	log.Infof("droid: response duration=%dms result_chars=%d (~%d tokens) turns=%d", result.DurationMs, resultLen, resultLen/4, result.NumTurns)

	response := convertDroidToOpenAIResponse(result, conv.Model)
	payload, err := json.Marshal(response)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to marshal response: %w", err)
	}

	return cliproxyexecutor.Response{Payload: payload}, nil
}

// executeWithSession uses session management for efficient multi-turn conversations
func (e *DroidExecutor) executeWithSession(ctx context.Context, apiKey string, conv *droidConversation) (*droidJSONResult, error) {
	if e.sessionManager == nil {
		return nil, fmt.Errorf("session manager not initialized")
	}

	// Need system prompt to identify session
	if conv.SystemPrompt == "" {
		return nil, fmt.Errorf("no system prompt for session management")
	}

	// Get or create session
	session, existed := e.sessionManager.GetOrCreateSession(conv.SystemPrompt, conv.Model)

	// Try to use existing pool or create one
	e.poolMu.Lock()
	pool := e.pool
	if pool == nil && e.cfg != nil && len(e.cfg.DroidKey) > 0 {
		droidCfg := e.cfg.DroidKey[0]
		if droidCfg.PoolEnabled {
			poolSize := droidCfg.PoolSize
			if poolSize <= 0 {
				poolSize = 3
			}
			cwd, _ := os.Getwd()
			pool = NewDroidProcessPool(poolSize, apiKey, conv.Model, cwd)
			e.pool = pool
			log.Infof("droid: lazy-initialized process pool with size %d", poolSize)
		}
	}
	e.poolMu.Unlock()

	if pool == nil {
		return nil, fmt.Errorf("pool not enabled")
	}

	// Get or create worker for this session
	session.mu.Lock()
	worker := session.Worker
	if worker != nil && worker.isAlive() {
		// Session has an existing worker, try to use it
		if worker.busy.CompareAndSwap(false, true) {
			// Successfully acquired the worker
			session.mu.Unlock()
		} else {
			// Worker is busy (concurrent request to same session), wait for it
			session.mu.Unlock()
			// Wait with timeout for worker to become available
			waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			for {
				select {
				case <-waitCtx.Done():
					return nil, fmt.Errorf("timeout waiting for session worker")
				case <-time.After(100 * time.Millisecond):
					if worker.busy.CompareAndSwap(false, true) {
						goto workerAcquired
					}
				}
			}
		}
	} else {
		// Need new worker from pool
		session.mu.Unlock()
		var err error
		worker, err = pool.Acquire(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to acquire worker: %w", err)
		}
		session.mu.Lock()
		session.Worker = worker
		existed = false // Need to send full context
		session.mu.Unlock()
	}
workerAcquired:

	startTime := time.Now()
	var response string
	var err error

	if !existed || len(session.GetMessages()) == 0 {
		// New session - send full context as first message
		log.Debugf("droid session: new session %s, sending full context (%d chars)", session.ID, len(conv.FullPrompt))
		response, err = worker.SendMessage(ctx, conv.FullPrompt)
		if err != nil {
			pool.ReleaseWithError(worker, err)
			session.mu.Lock()
			session.Worker = nil
			session.mu.Unlock()
			return nil, err
		}
		// Save messages to session
		session.SetMessages(conv.Messages)
	} else {
		// Existing session - find and send only new messages
		newMessages := session.FindNewMessages(conv.Messages)
		if len(newMessages) == 0 {
			return nil, fmt.Errorf("no new messages to send")
		}

		// Build prompt from new messages only
		var sb strings.Builder
		for _, msg := range newMessages {
			if msg.Role == "user" {
				sb.WriteString(msg.Content)
				sb.WriteString("\n")
			}
		}
		newPrompt := strings.TrimSpace(sb.String())

		log.Debugf("droid session: existing session %s, sending delta (%d chars vs %d full)", session.ID, len(newPrompt), len(conv.FullPrompt))
		response, err = worker.SendMessage(ctx, newPrompt)
		if err != nil {
			pool.ReleaseWithError(worker, err)
			session.mu.Lock()
			session.Worker = nil
			session.mu.Unlock()
			return nil, err
		}
		// Update session messages
		session.SetMessages(conv.Messages)
	}

	// Add assistant response to session
	session.AddMessages([]DroidMessage{{Role: "assistant", Content: response}})

	// Release worker back to pool (but keep reference in session for potential reuse)
	worker.busy.Store(false)

	duration := time.Since(startTime)
	log.Infof("droid session: response from session %s, duration=%dms chars=%d", session.ID, duration.Milliseconds(), len(response))

	return &droidJSONResult{
		Type:       "result",
		Subtype:    "success",
		IsError:    false,
		DurationMs: duration.Milliseconds(),
		NumTurns:   1,
		Result:     response,
		SessionID:  session.ID,
	}, nil
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

// droidConversation holds parsed conversation data
type droidConversation struct {
	Model           string
	ReasoningEffort string
	SystemPrompt    string
	Messages        []DroidMessage
	FullPrompt      string
}

func extractDroidConversation(payload []byte, defaultModel string) (*droidConversation, error) {
	var req droidOpenAIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}

	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		model = "claude-opus-4-5-20251101"
	}
	model, reasoningEffort := mapDroidModel(model)

	conv := &droidConversation{
		Model:           model,
		ReasoningEffort: reasoningEffort,
		Messages:        make([]DroidMessage, 0, len(req.Messages)),
	}

	// Extract system prompt (first message if it contains SYSTEM: or role=system)
	// and build messages list
	var sb strings.Builder
	for i, msg := range req.Messages {
		if msg.Content == "" {
			continue
		}

		conv.Messages = append(conv.Messages, DroidMessage{
			Role:    msg.Role,
			Content: msg.Content,
		})

		// First user message with SYSTEM: prefix or system role is the system prompt
		if i == 0 && (msg.Role == "system" || strings.HasPrefix(msg.Content, "SYSTEM:")) {
			conv.SystemPrompt = msg.Content
		}

		// Build full prompt for subprocess fallback
		role := strings.ToUpper(msg.Role)
		sb.WriteString(role)
		sb.WriteString(": ")
		sb.WriteString(msg.Content)
		sb.WriteString("\n\n")
	}

	conv.FullPrompt = strings.TrimSpace(sb.String())
	return conv, nil
}

// extractDroidPromptAndModel is a compatibility wrapper
func extractDroidPromptAndModel(payload []byte, defaultModel string) (prompt string, model string, reasoningEffort string, err error) {
	conv, err := extractDroidConversation(payload, defaultModel)
	if err != nil {
		return "", "", "", err
	}
	return conv.FullPrompt, conv.Model, conv.ReasoningEffort, nil
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
	// Use temporary file for prompt to avoid command line length limits
	tmpFile, err := os.CreateTemp("", "droid-prompt-*.txt")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(prompt); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}
	tmpFile.Close()

	args := []string{"exec", "--output-format", "json"}
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

	// Disable all tools for chat-only mode (see droidDisabledTools constant)
	args = append(args, "--disabled-tools", droidDisabledTools)

	// Pass prompt via file
	args = append(args, "-f", tmpFile.Name())

	cmd := exec.CommandContext(ctx, droidPath, args...)
	cmd.Env = append(os.Environ(), "FACTORY_API_KEY="+apiKey)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("droid exec failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("droid exec failed: %w", err)
	}

	var result droidJSONResult
	if err := json.Unmarshal(output, &result); err != nil {
		// If direct unmarshal fails, try to clean up output (sometimes there might be logs mixed in)
		// Find the last JSON object
		lastBrace := bytes.LastIndexByte(output, '}')
		firstBrace := bytes.LastIndexByte(output[:lastBrace+1], '{')
		if lastBrace > firstBrace && firstBrace >= 0 {
			if err2 := json.Unmarshal(output[firstBrace:lastBrace+1], &result); err2 == nil {
				return &result, nil
			}
		}
		return nil, fmt.Errorf("failed to parse droid output: %w (output: %s)", err, string(output))
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
	// Use temporary file for prompt
	tmpFile, err := os.CreateTemp("", "droid-prompt-stream-*.txt")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(prompt); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	tmpFile.Close()

	args := []string{"exec", "--output-format", "stream-json"}
	if model != "" {
		args = append(args, "-m", model)
	}
	if reasoningEffort != "" {
		args = append(args, "-r", reasoningEffort)
	} else {
		if model != "glm-4.6" {
			args = append(args, "-r", "off")
		}
	}

	args = append(args, "--disabled-tools", droidDisabledTools)
	args = append(args, "-f", tmpFile.Name())

	cmd := exec.CommandContext(ctx, droidPath, args...)
	cmd.Env = append(os.Environ(), "FACTORY_API_KEY="+apiKey)

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
