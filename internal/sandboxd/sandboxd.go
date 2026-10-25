// Package sandboxd is the client for the agent-sandbox runtime daemon that
// runs as PID 1 inside every sandbox: commands over its gRPC ProcessService,
// files over its REST API.
package sandboxd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	processv1 "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

// Image is pinned by index digest: upstream publishes only a moving
// latest-main tag from its staging registry.
const Image = "us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandboxd@sha256:141be71c5a3106cdd2cd3d33461acaa50a3e9945d3611239e91f803e06350bfa"

// Daemon paths and ports, the same in every sandbox.
const (
	ImageBinary = "/usr/local/bin/sandboxd"
	Workspace   = "/workspace"
	GRPCPort    = 9090
	RESTPort    = 8080

	readyTimeout = 90 * time.Second
)

// Args are the daemon flags; the token itself never appears, only its digest.
func Args(token string) []string {
	sum := sha256.Sum256([]byte(token))
	return []string{"--root-dir", Workspace, "--listen-host", "0.0.0.0", "--auth-token-sha256", hex.EncodeToString(sum[:])}
}

// NewToken makes a random bearer token for one provider or namespace.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Entry is one row of a directory listing.
type Entry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Type       string `json:"type"`
	ModifiedAt string `json:"modified_at"`
	Mode       string `json:"mode"`
}

type listing struct {
	Entries []Entry `json:"entries"`
}

// Error is a REST failure as the daemon reported it.
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// IsNotFound reports a 404 from the daemon.
func IsNotFound(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Status == http.StatusNotFound
}

// ErrNotDirectory is returned by List on a file path.
var ErrNotDirectory = errors.New("not a directory")

// Result is what a command produced.
type Result struct {
	Stdout   []byte
	Stderr   string
	ExitCode int
}

type bearer string

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}

func (bearer) RequireTransportSecurity() bool { return false }

// Client talks to one daemon.
type Client struct {
	conn  *grpc.ClientConn
	proc  processv1.ProcessServiceClient
	http  *http.Client
	rest  *url.URL
	token string
}

// Dial connects to a daemon and waits until it answers health checks.
func Dial(ctx context.Context, grpcAddr, restAddr, token string) (*Client, error) {
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithPerRPCCredentials(bearer(token)))
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:  conn,
		proc:  processv1.NewProcessServiceClient(conn),
		http:  &http.Client{},
		rest:  &url.URL{Scheme: "http", Host: restAddr},
		token: token,
	}
	deadline := time.Now().Add(readyTimeout)
	for {
		err := c.health(ctx)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			c.Close()
			return nil, fmt.Errorf("sandboxd did not become ready: %w", err)
		}
		select {
		case <-ctx.Done():
			c.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Close drops the gRPC connection.
func (c *Client) Close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *Client) health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rest.JoinPath("/v1/health").String(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("health: HTTP %d", res.StatusCode)
	}
	return nil
}

// Run executes argv in the workspace. After timeout the daemon kills the whole
// process group and the result reports exit code 124.
func (c *Client) Run(ctx context.Context, argv []string, timeout time.Duration) (Result, error) {
	stream, err := c.proc.Start(ctx, &processv1.StartRequest{Config: &processv1.ProcessConfig{Command: argv, Cwd: proto.String(Workspace)}})
	if err != nil {
		return Result{}, fmt.Errorf("start %s: %w", strings.Join(argv, " "), err)
	}
	var stdout, stderr bytes.Buffer
	var timedOut atomic.Bool
	var timer *time.Timer
	code := -1
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if timer != nil {
				timer.Stop()
			}
			return Result{}, fmt.Errorf("run %s: %w", strings.Join(argv, " "), err)
		}
		switch {
		case ev.GetInit() != nil:
			if timeout > 0 {
				pid := ev.GetInit().GetProcessId()
				timer = time.AfterFunc(timeout, func() {
					timedOut.Store(true)
					kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_, _ = c.proc.SendSignal(kctx, &processv1.SendSignalRequest{ProcessId: pid, Signal: processv1.Signal_SIGNAL_SIGKILL})
				})
			}
		case ev.GetExit() != nil:
			code = int(ev.GetExit().GetExitCode())
		default:
			stdout.Write(ev.GetStdout())
			stderr.Write(ev.GetStderr())
		}
		if ev.GetExit() != nil {
			break
		}
	}
	if timer != nil {
		timer.Stop()
	}
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.String(), ExitCode: code}
	if timedOut.Load() {
		res.ExitCode = 124
		res.Stderr = strings.TrimSpace(res.Stderr + "\ncommand timed out after " + timeout.String())
	}
	return res, nil
}

func (c *Client) do(ctx context.Context, method, p string, query url.Values, body io.Reader, size int64) (*http.Response, error) {
	u := c.rest.JoinPath("/v1/files/", p)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 300 {
		return res, nil
	}
	defer func() { _ = res.Body.Close() }()
	e := &Error{Status: res.StatusCode}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(e); err != nil || e.Message == "" {
		e.Message = fmt.Sprintf("sandboxd: HTTP %d", res.StatusCode)
	}
	return nil, e
}

// Open streams a file; the caller closes the reader.
func (c *Client) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	res, err := c.do(ctx, http.MethodGet, p, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		_ = res.Body.Close()
		return nil, fmt.Errorf("%s is a directory", p)
	}
	return res.Body, nil
}

// List returns the entries of a directory.
func (c *Client) List(ctx context.Context, p string) ([]Entry, error) {
	res, err := c.do(ctx, http.MethodGet, p, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		return nil, ErrNotDirectory
	}
	var l listing
	if err := json.NewDecoder(res.Body).Decode(&l); err != nil {
		return nil, err
	}
	return l.Entries, nil
}

// Stat describes one path; "." is the workspace itself.
func (c *Client) Stat(ctx context.Context, p string) (Entry, error) {
	if p == "." {
		return Entry{Name: ".", Type: "directory", Mode: "0755"}, nil
	}
	entries, err := c.List(ctx, path.Dir(p))
	if errors.Is(err, ErrNotDirectory) {
		return Entry{}, &Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: p + " does not exist"}
	}
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.Name == path.Base(p) {
			return e, nil
		}
	}
	return Entry{}, &Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: p + " does not exist"}
}

// Write stores r at p atomically, creating parent directories. size -1 streams with unknown length.
func (c *Client) Write(ctx context.Context, p string, r io.Reader, size int64) error {
	res, err := c.do(ctx, http.MethodPut, p, nil, r, size)
	if err != nil {
		return err
	}
	return res.Body.Close()
}

// Remove deletes a file, or a whole tree when recursive.
func (c *Client) Remove(ctx context.Context, p string, recursive bool) error {
	q := url.Values{}
	if recursive {
		q.Set("recursive", "true")
	}
	res, err := c.do(ctx, http.MethodDelete, p, q, nil, 0)
	if err != nil {
		return err
	}
	return res.Body.Close()
}
