package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// DroidWorker represents a single persistent droid process
type DroidWorker struct {
	id           int
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *bufio.Reader
	stderr       io.ReadCloser
	sessionID    string
	apiKey       string
	model        string
	cwd          string
	busy         atomic.Bool
	requestCount int64
	lastUsed     time.Time
	mu           sync.Mutex
	msgID        int64
}

// DroidProcessPool manages a pool of persistent droid processes
type DroidProcessPool struct {
	workers     []*DroidWorker
	size        int
	apiKey      string
	model       string
	cwd         string
	mu          sync.Mutex
	workerCond  *sync.Cond
	closed      bool
	maxRequests int64 // restart worker after this many requests
}

// JSON-RPC structures
type jsonRPCRequest struct {
	JSONRPC           string      `json:"jsonrpc"`
	FactoryAPIVersion string      `json:"factoryApiVersion"`
	Type              string      `json:"type"`
	ID                string      `json:"id"`
	Method            string      `json:"method"`
	Params            interface{} `json:"params"`
}

type initSessionParams struct {
	MachineID string `json:"machineId"`
	CWD       string `json:"cwd"`
}

type addUserMessageParams struct {
	Text string `json:"text"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initSessionResult struct {
	SessionID string `json:"sessionId"`
}

type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	FinalText string `json:"finalText,omitempty"`
	Text      string `json:"text,omitempty"`
	Role      string `json:"role,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// NewDroidProcessPool creates a new pool of droid workers
func NewDroidProcessPool(size int, apiKey, model, cwd string) *DroidProcessPool {
	if size <= 0 {
		size = 3
	}
	pool := &DroidProcessPool{
		workers:     make([]*DroidWorker, 0, size),
		size:        size,
		apiKey:      apiKey,
		model:       model,
		cwd:         cwd,
		maxRequests: 100, // restart after 100 requests to prevent memory leaks
	}
	pool.workerCond = sync.NewCond(&pool.mu)
	return pool
}

// Acquire gets an available worker from the pool or creates a new one
func (p *DroidProcessPool) Acquire(ctx context.Context) (*DroidWorker, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, fmt.Errorf("pool is closed")
	}

	// Try to find an idle worker
	for _, w := range p.workers {
		if !w.busy.Load() && w.isAlive() {
			w.busy.Store(true)
			w.lastUsed = time.Now()
			log.Debugf("droid pool: acquired existing worker %d", w.id)
			return w, nil
		}
	}

	// Create new worker if pool not full
	if len(p.workers) < p.size {
		worker, err := p.createWorker(ctx)
		if err != nil {
			return nil, err
		}
		worker.busy.Store(true)
		p.workers = append(p.workers, worker)
		log.Debugf("droid pool: created new worker %d, pool size: %d", worker.id, len(p.workers))
		return worker, nil
	}

	// Wait for available worker with timeout
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	for {
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("timeout waiting for available worker")
		default:
		}

		for _, w := range p.workers {
			if !w.busy.Load() && w.isAlive() {
				w.busy.Store(true)
				w.lastUsed = time.Now()
				log.Debugf("droid pool: acquired worker %d after wait", w.id)
				return w, nil
			}
		}

		// Brief sleep before retry
		p.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		p.mu.Lock()

		if p.closed {
			return nil, fmt.Errorf("pool is closed")
		}
	}
}

// Release returns a worker to the pool
func (p *DroidProcessPool) Release(worker *DroidWorker) {
	if worker == nil {
		return
	}

	worker.requestCount++

	// Restart worker if it exceeded max requests
	if worker.requestCount >= p.maxRequests {
		log.Debugf("droid pool: restarting worker %d after %d requests", worker.id, worker.requestCount)
		worker.Close()
		p.mu.Lock()
		// Remove dead worker, new one will be created on next Acquire
		for i, w := range p.workers {
			if w == worker {
				p.workers = append(p.workers[:i], p.workers[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		return
	}

	worker.busy.Store(false)
	worker.lastUsed = time.Now()
	log.Debugf("droid pool: released worker %d", worker.id)
}

// ReleaseWithError releases a worker and marks it for restart if there was an error
func (p *DroidProcessPool) ReleaseWithError(worker *DroidWorker, err error) {
	if worker == nil {
		return
	}

	if err != nil {
		log.Debugf("droid pool: closing worker %d due to error: %v", worker.id, err)
		worker.Close()
		p.mu.Lock()
		for i, w := range p.workers {
			if w == worker {
				p.workers = append(p.workers[:i], p.workers[i+1:]...)
				break
			}
		}
		p.mu.Unlock()
		return
	}

	p.Release(worker)
}

// Close shuts down all workers in the pool
func (p *DroidProcessPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true
	for _, w := range p.workers {
		w.Close()
	}
	p.workers = nil
	log.Debug("droid pool: closed")
}

// Stats returns pool statistics
func (p *DroidProcessPool) Stats() (total, busy, idle int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	total = len(p.workers)
	for _, w := range p.workers {
		if w.busy.Load() {
			busy++
		} else {
			idle++
		}
	}
	return
}

var workerIDCounter int64

func (p *DroidProcessPool) createWorker(ctx context.Context) (*DroidWorker, error) {
	id := int(atomic.AddInt64(&workerIDCounter, 1))

	args := []string{
		"exec",
		"--input-format", "stream-jsonrpc",
		"--output-format", "stream-jsonrpc",
		"--auto", "low",
	}
	if p.model != "" {
		args = append(args, "-m", p.model)
	}

	cmd := exec.CommandContext(ctx, "droid", args...)
	cmd.Env = append(os.Environ(), "FACTORY_API_KEY="+p.apiKey)
	cmd.Dir = p.cwd

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("failed to start droid process: %w", err)
	}

	worker := &DroidWorker{
		id:       id,
		cmd:      cmd,
		stdin:    stdin,
		stdout:   bufio.NewReader(stdout),
		stderr:   stderr,
		apiKey:   p.apiKey,
		model:    p.model,
		cwd:      p.cwd,
		lastUsed: time.Now(),
	}

	// Initialize session
	if err := worker.initSession(ctx); err != nil {
		worker.Close()
		return nil, fmt.Errorf("failed to initialize session: %w", err)
	}

	log.Debugf("droid pool: worker %d started with session %s", id, worker.sessionID)
	return worker, nil
}

func (w *DroidWorker) initSession(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.msgID++
	req := jsonRPCRequest{
		JSONRPC:           "2.0",
		FactoryAPIVersion: "1.0.0",
		Type:              "request",
		ID:                fmt.Sprintf("%d", w.msgID),
		Method:            "droid.initialize_session",
		Params: initSessionParams{
			MachineID: fmt.Sprintf("pool-worker-%d", w.id),
			CWD:       w.cwd,
		},
	}

	if err := w.sendRequest(req); err != nil {
		return err
	}

	// Read response
	resp, err := w.readResponse(ctx, req.ID)
	if err != nil {
		return err
	}

	if resp.Error != nil {
		return fmt.Errorf("init session error: %s", resp.Error.Message)
	}

	var result initSessionResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("failed to parse init result: %w", err)
	}

	w.sessionID = result.SessionID
	return nil
}

// SendMessage sends a user message and returns the assistant response
func (w *DroidWorker) SendMessage(ctx context.Context, text string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.msgID++
	msgID := fmt.Sprintf("%d", w.msgID)

	req := jsonRPCRequest{
		JSONRPC:           "2.0",
		FactoryAPIVersion: "1.0.0",
		Type:              "request",
		ID:                msgID,
		Method:            "droid.add_user_message",
		Params: addUserMessageParams{
			Text: text,
		},
	}

	if err := w.sendRequest(req); err != nil {
		return "", err
	}

	// Read until we get completion event
	return w.readUntilCompletion(ctx, msgID)
}

func (w *DroidWorker) sendRequest(req jsonRPCRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	data = append(data, '\n')
	if _, err := w.stdin.Write(data); err != nil {
		return fmt.Errorf("failed to write to stdin: %w", err)
	}

	return nil
}

func (w *DroidWorker) readResponse(ctx context.Context, expectedID string) (*jsonRPCResponse, error) {
	type readResult struct {
		line string
		err  error
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Read with timeout using goroutine
		lineCh := make(chan readResult, 1)
		go func() {
			line, err := w.stdout.ReadString('\n')
			lineCh <- readResult{line, err}
		}()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timeout waiting for response")
		case result := <-lineCh:
			if result.err != nil {
				return nil, fmt.Errorf("failed to read response: %w", result.err)
			}

			var resp jsonRPCResponse
			if err := json.Unmarshal([]byte(result.line), &resp); err != nil {
				log.Debugf("droid pool: skipping malformed line: %s", result.line)
				continue
			}

			if resp.ID == expectedID {
				return &resp, nil
			}
		}
	}
}

func (w *DroidWorker) readUntilCompletion(ctx context.Context, msgID string) (string, error) {
	type readResult struct {
		line string
		err  error
	}

	var finalText string
	timeout := 120 * time.Second // 2 min timeout for LLM response

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		// Read with timeout using goroutine
		lineCh := make(chan readResult, 1)
		go func() {
			line, err := w.stdout.ReadString('\n')
			lineCh <- readResult{line, err}
		}()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(timeout):
			return "", fmt.Errorf("timeout waiting for completion")
		case result := <-lineCh:
			if result.err != nil {
				return "", fmt.Errorf("failed to read: %w", result.err)
			}

			// Try to parse as JSON-RPC response first
			var resp jsonRPCResponse
			if err := json.Unmarshal([]byte(result.line), &resp); err == nil {
				if resp.Error != nil {
					return "", fmt.Errorf("droid error: %s", resp.Error.Message)
				}
				// Initial ack response, continue reading
				continue
			}

			// Try to parse as stream event
			var event streamEvent
			if err := json.Unmarshal([]byte(result.line), &event); err != nil {
				log.Debugf("droid pool: skipping unknown line: %s", result.line)
				continue
			}

			// Check for errors
			if event.Error != nil {
				return "", fmt.Errorf("stream error: %s", event.Error.Message)
			}

			// Check for completion
			if event.Type == "completion" {
				finalText = event.FinalText
				return finalText, nil
			}

			// Check for assistant message (fallback)
			if event.Type == "message" && event.Role == "assistant" && event.Text != "" {
				finalText = event.Text
			}
		}
	}
}

func (w *DroidWorker) isAlive() bool {
	if w.cmd == nil || w.cmd.Process == nil {
		return false
	}
	// Check if process is still running
	// On Windows, this doesn't work perfectly but it's a reasonable check
	return w.cmd.ProcessState == nil
}

// Close terminates the worker process
func (w *DroidWorker) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stdin != nil {
		w.stdin.Close()
	}
	if w.stderr != nil {
		w.stderr.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
		w.cmd.Wait()
	}
	log.Debugf("droid pool: worker %d closed", w.id)
}
