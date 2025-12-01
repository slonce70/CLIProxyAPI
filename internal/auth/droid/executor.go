// Package droid provides a provider executor for Factory Droid CLI.
// It wraps the `droid exec` command to provide OpenAI-compatible API access
// to Factory's AI models (Claude, GPT, GLM, etc.).
package droid

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

const (
	ProviderID = "droid"
)

// Executor implements auth.ProviderExecutor for Factory Droid.
type Executor struct {
	droidPath string
}

// NewExecutor creates a new Droid executor.
// If droidPath is empty, it will use "droid" from PATH.
func NewExecutor(droidPath string) *Executor {
	if droidPath == "" {
		droidPath = "droid"
	}
	return &Executor{droidPath: droidPath}
}

// Identifier returns the provider key for this executor.
func (e *Executor) Identifier() string {
	return ProviderID
}

// Execute handles non-streaming execution via droid exec -o json.
func (e *Executor) Execute(ctx context.Context, a *auth.Auth, req executor.Request, opts executor.Options) (executor.Response, error) {
	apiKey := getAPIKey(a)
	if apiKey == "" {
		return executor.Response{}, fmt.Errorf("droid: missing FACTORY_API_KEY")
	}

	// Extract prompt from OpenAI-style request
	prompt, model, err := extractPromptAndModel(req.Payload, req.Model)
	if err != nil {
		return executor.Response{}, fmt.Errorf("droid: failed to extract prompt: %w", err)
	}

	// Execute droid command
	result, err := executeDroid(ctx, e.droidPath, apiKey, model, prompt, false)
	if err != nil {
		return executor.Response{}, fmt.Errorf("droid: execution failed: %w", err)
	}

	// Convert to OpenAI response format
	response := convertToOpenAIResponse(result, model)
	payload, err := json.Marshal(response)
	if err != nil {
		return executor.Response{}, fmt.Errorf("droid: failed to marshal response: %w", err)
	}

	return executor.Response{Payload: payload}, nil
}

// ExecuteStream handles streaming execution via droid exec -o stream-json.
func (e *Executor) ExecuteStream(ctx context.Context, a *auth.Auth, req executor.Request, opts executor.Options) (<-chan executor.StreamChunk, error) {
	apiKey := getAPIKey(a)
	if apiKey == "" {
		return nil, fmt.Errorf("droid: missing FACTORY_API_KEY")
	}

	prompt, model, err := extractPromptAndModel(req.Payload, req.Model)
	if err != nil {
		return nil, fmt.Errorf("droid: failed to extract prompt: %w", err)
	}

	ch := make(chan executor.StreamChunk, 100)

	go func() {
		defer close(ch)
		err := executeDroidStream(ctx, e.droidPath, apiKey, model, prompt, ch)
		if err != nil {
			ch <- executor.StreamChunk{Err: err}
		}
	}()

	return ch, nil
}

// Refresh is not needed for API key authentication.
func (e *Executor) Refresh(ctx context.Context, a *auth.Auth) (*auth.Auth, error) {
	// API keys don't expire, no refresh needed
	return a, nil
}

// CountTokens is not supported by Droid.
func (e *Executor) CountTokens(ctx context.Context, a *auth.Auth, req executor.Request, opts executor.Options) (executor.Response, error) {
	return executor.Response{}, fmt.Errorf("droid: token counting not supported")
}

// getAPIKey extracts the Factory API key from auth attributes or environment.
func getAPIKey(a *auth.Auth) string {
	if a != nil && a.Attributes != nil {
		if key := a.Attributes["api_key"]; key != "" {
			return key
		}
	}
	return os.Getenv("FACTORY_API_KEY")
}

// OpenAI request structures
type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// extractPromptAndModel parses OpenAI-style request and extracts prompt.
func extractPromptAndModel(payload []byte, defaultModel string) (string, string, error) {
	var req openAIRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return "", "", err
	}

	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		model = "claude-opus-4-5-20251101" // Default Droid model
	}
	
	// Map proxy model ID to native Droid model ID
	model = MapToNativeModel(model)

	// Build prompt from messages
	var promptBuilder strings.Builder
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			promptBuilder.WriteString("System: ")
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString("\n\n")
		} else if msg.Role == "user" {
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString("\n")
		} else if msg.Role == "assistant" {
			promptBuilder.WriteString("Assistant: ")
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString("\n\n")
		}
	}

	return strings.TrimSpace(promptBuilder.String()), model, nil
}

// OpenAI response structures
type openAIResponse struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []openAIChoice  `json:"choices"`
	Usage   *openAIUsage    `json:"usage,omitempty"`
}

type openAIChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// convertToOpenAIResponse converts Droid result to OpenAI format.
func convertToOpenAIResponse(result *DroidResult, model string) *openAIResponse {
	return &openAIResponse{
		ID:      "chatcmpl-droid-" + result.SessionID,
		Object:  "chat.completion",
		Created: result.Timestamp / 1000, // Convert ms to seconds
		Model:   model,
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMessage{
					Role:    "assistant",
					Content: result.Result,
				},
				FinishReason: "stop",
			},
		},
	}
}

// OpenAI streaming structures
type openAIStreamChunk struct {
	ID      string               `json:"id"`
	Object  string               `json:"object"`
	Created int64                `json:"created"`
	Model   string               `json:"model"`
	Choices []openAIStreamChoice `json:"choices"`
}

type openAIStreamChoice struct {
	Index        int                `json:"index"`
	Delta        openAIStreamDelta  `json:"delta"`
	FinishReason *string            `json:"finish_reason"`
}

type openAIStreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

func convertToOpenAIStreamChunk(text string, model string, sessionID string, isFirst bool, isDone bool) []byte {
	chunk := openAIStreamChunk{
		ID:      "chatcmpl-droid-" + sessionID,
		Object:  "chat.completion.chunk",
		Created: 0,
		Model:   model,
		Choices: []openAIStreamChoice{
			{
				Index: 0,
				Delta: openAIStreamDelta{},
			},
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
	return append([]byte("data: "), append(data, '\n', '\n')...)
}
