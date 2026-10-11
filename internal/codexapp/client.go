package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xiaolai/recite/internal/testguard"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xiaolai/recite/internal/logx"
	"github.com/xiaolai/recite/internal/openai"
)

const (
	// DefaultTimeout bounds every app-server call (design §5.1a, AUTH-01#2).
	DefaultTimeout = 10 * time.Second
	// killGrace is how long Close waits after SIGTERM before SIGKILL (AUTH-01).
	killGrace = 2 * time.Second
	// stderrCap bounds the retained child stderr (AUTH-01).
	stderrCap = 64 << 10
	// maxLine bounds one JSON-RPC message from the child.
	maxLine = 4 << 20
)

// Config describes how to start the child. Bin must be the absolute path from Resolve.
type Config struct {
	Bin     string
	Home    string        // passed as CODEX_HOME when non-empty
	Timeout time.Duration // per call; 0 means DefaultTimeout
	Logger  *slog.Logger  // receives the scrubbed child stderr at debug level; nil discards
}

// RPCError is a JSON-RPC error reply. Message is scrubbed and kept out of Error(),
// because a misbehaving child may put a credential there (AUTH-05#2).
type RPCError struct {
	Code    int
	Message string
}

// CodeMethodNotFound is the JSON-RPC code an older Codex returns for an unknown method.
const CodeMethodNotFound = -32601

func (e *RPCError) Error() string { return fmt.Sprintf("JSON-RPC error %d", e.Code) }

// Client is one `codex app-server --listen stdio://` child, alive until Close.
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	timeout time.Duration
	logger  *slog.Logger
	stderr  boundedBuffer
	agent   string // initialize userAgent, scrubbed

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	failure error         // set once the child is unusable
	dead    chan struct{} // closed when the reader stops
	exited  chan struct{} // closed when the child is reaped
	closeMu sync.Once
}

type response struct {
	result json.RawMessage
	err    error
}

type wireMsg struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// codexError builds a codex-error class failure; cause is scrubbed by openai.Error.
func codexError(msg string, cause error) error {
	return &openai.Error{Class: openai.ClassCodexError, Message: msg, Err: cause}
}

// Start spawns the child and completes the initialize handshake.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if !filepath.IsAbs(cfg.Bin) {
		return nil, fmt.Errorf("codexapp: executable path %q is not absolute", cfg.Bin)
	}
	if testguard.Active() && !testguard.TempPath(cfg.Bin) {
		return nil, fmt.Errorf("codexapp: a non-live test tried to run %s; tests must use the fake codex", cfg.Bin)
	}
	c := &Client{
		timeout: cfg.Timeout,
		logger:  cfg.Logger,
		pending: map[int64]chan response{},
		dead:    make(chan struct{}),
		exited:  make(chan struct{}),
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	// No CommandContext: the child must outlive ctx and is reaped only by Close.
	cmd := exec.Command(cfg.Bin, "app-server", "--listen", "stdio://")
	cmd.Env = childEnv(os.Environ(), cfg.Home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, codexError("Codex app-server could not be started", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, codexError("Codex app-server could not be started", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, codexError("Codex app-server could not be started", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, codexError("Codex app-server could not be started", err)
	}
	c.cmd, c.stdin = cmd, stdin

	var drained sync.WaitGroup
	drained.Add(2)
	go func() { defer drained.Done(); c.readLoop(stdout) }()
	go func() { defer drained.Done(); _, _ = io.Copy(&c.stderr, stderr) }()
	go func() {
		drained.Wait() // Wait closes the pipes, so it must follow the readers
		_ = cmd.Wait()
		close(c.exited)
	}()

	init := map[string]any{
		"clientInfo":   map[string]any{"name": "recite", "version": "0"},
		"capabilities": map[string]any{},
	}
	var hello struct {
		UserAgent string `json:"userAgent"`
	}
	if err := c.Call(ctx, "initialize", init, &hello); err != nil {
		_ = c.Close()
		return nil, err
	}
	c.agent = logx.Scrub(hello.UserAgent)
	if err := c.notify("initialized"); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// childEnv is env minus OPENAI_API_KEY, with CODEX_HOME replaced when home is set.
// Nothing is ever added except CODEX_HOME, so no secret can reach the child's environment.
func childEnv(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(name, "OPENAI_API_KEY") || (home != "" && strings.EqualFold(name, "CODEX_HOME")) {
			continue
		}
		out = append(out, kv)
	}
	if home != "" {
		out = append(out, "CODEX_HOME="+home)
	}
	return out
}

// UserAgent returns the userAgent of the initialize result, "" when absent.
func (c *Client) UserAgent() string { return c.agent }

// PID returns the child's process ID.
func (c *Client) PID() int { return c.cmd.Process.Pid }

// readLoop matches replies to pending calls until the child's stdout ends or misbehaves.
func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var failure error
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m wireMsg
		if err := json.Unmarshal(line, &m); err != nil {
			// The line is never quoted: it may carry a credential (AUTH-05#2).
			failure = codexError("Codex app-server sent malformed JSON", nil)
			break
		}
		if m.ID == nil || m.Method != "" {
			continue // a notification or a server request; recite needs neither
		}
		c.deliver(*m.ID, m)
	}
	if failure == nil {
		if err := sc.Err(); err != nil {
			failure = codexError("Codex app-server output could not be read", nil)
		} else {
			failure = codexError("Codex app-server exited unexpectedly", nil)
		}
	}
	c.fail(failure)
	_, _ = io.Copy(io.Discard, r) // keep the child from blocking on a full pipe
}

func (c *Client) deliver(id int64, m wireMsg) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return
	}
	var resp response
	if m.Error != nil {
		resp.err = &RPCError{Code: m.Error.Code, Message: logx.Scrub(m.Error.Message)}
	} else {
		resp.result = m.Result
	}
	ch <- resp // buffered, never blocks
}

// fail marks the client unusable and releases every pending call.
func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.failure == nil {
		c.failure = err
		close(c.dead)
	}
	c.mu.Unlock()
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("codexapp: encode: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return codexError("Codex app-server is not accepting requests", nil)
	}
	return nil
}

func (c *Client) notify(method string) error {
	return c.write(map[string]any{"method": method})
}

// Call sends one request and decodes the result into out (when non-nil). It fails with a
// codex-error class error after the per-call timeout, on a crash or on malformed output; a
// JSON-RPC error reply is returned as a codex-error wrapping *RPCError.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	c.mu.Lock()
	if c.failure != nil {
		err := c.failure
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if params == nil {
		params = map[string]any{}
	}
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.err != nil {
			return codexError("Codex app-server rejected "+method, resp.err)
		}
		if out != nil {
			if err := json.Unmarshal(resp.result, out); err != nil {
				return codexError("Codex app-server sent an unexpected "+method+" result", nil)
			}
		}
		return nil
	case <-c.dead:
		c.mu.Lock()
		err := c.failure
		c.mu.Unlock()
		return err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return codexError(fmt.Sprintf("Codex app-server did not answer %s within %s", method, c.timeout), nil)
		}
		return ctx.Err()
	}
}

// Close ends the child: stdin is closed, then SIGTERM, then SIGKILL after 2 s. The
// retained stderr is logged through the scrubber. Safe to call more than once.
func (c *Client) Close() error {
	c.closeMu.Do(func() {
		_ = c.stdin.Close()
		if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			_ = c.cmd.Process.Kill()
		}
		select {
		case <-c.exited:
		case <-time.After(killGrace):
			_ = c.cmd.Process.Kill()
			<-c.exited
		}
		if s := c.stderr.String(); s != "" {
			c.logger.Debug("codex app-server stderr", "stderr", logx.Scrub(s))
		}
	})
	return nil
}

// boundedBuffer keeps the first stderrCap bytes and discards the rest.
type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := stderrCap - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
