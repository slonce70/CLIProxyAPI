package droid

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// DroidResult represents the JSON output from droid exec -o json.
type DroidResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	DurationMs int64  `json:"duration_ms"`
	NumTurns   int    `json:"num_turns"`
	Result     string `json:"result"`
	SessionID  string `json:"session_id"`
	Timestamp  int64  `json:"-"`
}

// DroidStreamEvent represents events from droid exec -o stream-json.
type DroidStreamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	Role      string `json:"role,omitempty"`
	Text      string `json:"text,omitempty"`
	FinalText string `json:"finalText,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`
	NumTurns  int    `json:"numTurns,omitempty"`
	DurationMs int64 `json:"durationMs,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	ID        string `json:"id,omitempty"`
	ToolName  string `json:"toolName,omitempty"`
}

// executeDroid runs droid exec with JSON output and returns parsed result.
func executeDroid(ctx context.Context, droidPath, apiKey, model, prompt string, stream bool) (*DroidResult, error) {
	args := []string{"exec", "-o", "json"}
	
	if model != "" {
		args = append(args, "-m", model)
	}
	
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, droidPath, args...)
	cmd.Env = append(cmd.Environ(), "FACTORY_API_KEY="+apiKey)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("droid exec failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("droid exec failed: %w", err)
	}

	var result DroidResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to parse droid output: %w", err)
	}

	result.Timestamp = time.Now().UnixMilli()

	if result.IsError {
		return nil, fmt.Errorf("droid error: %s", result.Result)
	}

	return &result, nil
}

// executeDroidStream runs droid exec with stream-json output and sends chunks to channel.
func executeDroidStream(ctx context.Context, droidPath, apiKey, model, prompt string, ch chan<- executor.StreamChunk) error {
	args := []string{"exec", "-o", "stream-json"}
	
	if model != "" {
		args = append(args, "-m", model)
	}
	
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, droidPath, args...)
	cmd.Env = append(cmd.Environ(), "FACTORY_API_KEY="+apiKey)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start droid: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	var sessionID string
	isFirst := true
	var lastText string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var event DroidStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // Skip malformed lines
		}

		switch event.Type {
		case "system":
			sessionID = event.SessionID
			// Send initial role chunk
			chunk := convertToOpenAIStreamChunk("", model, sessionID, true, false)
			ch <- executor.StreamChunk{Payload: chunk}
			isFirst = false

		case "message":
			if event.Role == "assistant" && event.Text != "" {
				// Only send new content (delta)
				newContent := event.Text
				if lastText != "" && strings.HasPrefix(newContent, lastText) {
					newContent = strings.TrimPrefix(newContent, lastText)
				}
				if newContent != "" {
					chunk := convertToOpenAIStreamChunk(newContent, model, sessionID, isFirst, false)
					ch <- executor.StreamChunk{Payload: chunk}
					isFirst = false
				}
				lastText = event.Text
			}

		case "completion":
			// Send final content if any
			if event.FinalText != "" && event.FinalText != lastText {
				newContent := event.FinalText
				if lastText != "" && strings.HasPrefix(newContent, lastText) {
					newContent = strings.TrimPrefix(newContent, lastText)
				}
				if newContent != "" {
					chunk := convertToOpenAIStreamChunk(newContent, model, sessionID, isFirst, false)
					ch <- executor.StreamChunk{Payload: chunk}
				}
			}
			// Send done chunk
			doneChunk := convertToOpenAIStreamChunk("", model, sessionID, false, true)
			ch <- executor.StreamChunk{Payload: doneChunk}
			// Send [DONE] marker
			ch <- executor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading droid output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("droid exec failed with code %d", exitErr.ExitCode())
		}
		return fmt.Errorf("droid exec failed: %w", err)
	}

	return nil
}
