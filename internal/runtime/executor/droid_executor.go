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
}

func NewDroidExecutor(cfg *config.Config) *DroidExecutor {
	return &DroidExecutor{cfg: cfg, droidPath: "droid"}
}

func (e *DroidExecutor) Identifier() string { return "droid" }

func (e *DroidExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey := getDroidAPIKey(auth)
	if apiKey == "" {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: missing FACTORY_API_KEY")
	}

	prompt, model, err := extractDroidPromptAndModel(req.Payload, req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("droid: failed to extract prompt: %w", err)
	}

	result, err := executeDroidJSON(ctx, e.droidPath, apiKey, model, prompt)
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

func (e *DroidExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	apiKey := getDroidAPIKey(auth)
	if apiKey == "" {
		return nil, fmt.Errorf("droid: missing FACTORY_API_KEY")
	}

	prompt, model, err := extractDroidPromptAndModel(req.Payload, req.Model)
	if err != nil {
		return nil, fmt.Errorf("droid: failed to extract prompt: %w", err)
	}

	ch := make(chan cliproxyexecutor.StreamChunk, 100)

	go func() {
		defer close(ch)
		if err := executeDroidStreamJSON(ctx, e.droidPath, apiKey, model, prompt, ch); err != nil {
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

func extractDroidPromptAndModel(payload []byte, defaultModel string) (string, string, error) {
	var req droidOpenAIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return "", "", err
	}

	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		model = "claude-opus-4-5-20251101"
	}
	model = mapDroidModel(model)

	var promptBuilder strings.Builder
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			promptBuilder.WriteString("System: ")
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString("\n\n")
		case "user":
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString("\n")
		case "assistant":
			promptBuilder.WriteString("Assistant: ")
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString("\n\n")
		}
	}

	return strings.TrimSpace(promptBuilder.String()), model, nil
}

func mapDroidModel(model string) string {
	modelMap := map[string]string{
		"droid-glm-4.6":           "glm-4.6",
		"droid-claude-haiku-4.5":  "claude-haiku-4-5-20251001",
		"droid-claude-sonnet-4.5": "claude-sonnet-4-5-20250929",
		"droid-claude-opus-4.5":   "claude-opus-4-5-20251101",
		"droid-gpt-5.1-codex":     "gpt-5.1-codex",
		"droid-gemini-3-pro":      "gemini-3-pro-preview",
	}
	if native, ok := modelMap[model]; ok {
		return native
	}
	return model
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

func executeDroidJSON(ctx context.Context, droidPath, apiKey, model, prompt string) (*droidJSONResult, error) {
	args := []string{"exec", "-o", "json"}
	if model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, prompt)

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
		return nil, fmt.Errorf("failed to parse droid output: %w", err)
	}

	if result.IsError {
		return nil, fmt.Errorf("droid error: %s", result.Result)
	}

	return &result, nil
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

func executeDroidStreamJSON(ctx context.Context, droidPath, apiKey, model, prompt string, ch chan<- cliproxyexecutor.StreamChunk) error {
	args := []string{"exec", "-o", "stream-json"}
	if model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, prompt)

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
