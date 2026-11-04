// Package sandboxd is the client for the agent-sandbox runtime daemon that
// runs as PID 1 inside every sandbox: commands over its gRPC ProcessService,
// files over its REST API.
package sandboxd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
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
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	processv1 "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

// Upstream publishes only a moving latest-main tag from its staging registry,
// so the index digest is what is pinned. Image is our mirror of exactly that
// digest, copied by .github/workflows/mirror-sandboxd.yaml; --sandboxd-image
// replaces it, for example with our FIPS build.
const (
	digest   = "sha256:141be71c5a3106cdd2cd3d33461acaa50a3e9945d3611239e91f803e06350bfa"
	Upstream = "us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/sandboxd@" + digest
)

var Image = "ghcr.io/hatchet-dev/sandboxd@" + digest

const (
	ImageBinary = "/usr/local/bin/sandboxd"
	Workspace   = "/workspace"
	GRPCPort    = 9090
	RESTPort    = 8080

	readyTimeout = 90 * time.Second

	// MaxOutputBytes caps what a command's stdout and stderr may each occupy in
	// provider memory; the rest is dropped and the result says so.
	MaxOutputBytes = 1 << 20
)

// Args are the daemon flags; the token itself never appears, only its digest.
func Args(token, listenHost string) []string {
	sum := sha256.Sum256([]byte(token))
	return []string{"--root-dir", Workspace, "--listen-host", listenHost, "--auth-token-sha256", hex.EncodeToString(sum[:])}
}

func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

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

type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func IsNotFound(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Status == http.StatusNotFound
}

var ErrNotDirectory = errors.New("not a directory")

type Result struct {
	Stdout   []byte
	Stderr   string
	ExitCode int
}

type capped struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (w *capped) Write(p []byte) (int, error) {
	if room := w.limit - w.buf.Len(); len(p) > room {
		w.buf.Write(p[:room])
		w.over = true
	} else {
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *capped) note(stderr, stream string) string {
	if !w.over {
		return stderr
	}
	return strings.TrimSpace(stderr + fmt.Sprintf("\n%s truncated at %d bytes", stream, w.limit))
}

type bearer string

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + string(b)}, nil
}

func (bearer) RequireTransportSecurity() bool { return false }

type Client struct {
	conn  *grpc.ClientConn
	proc  processv1.ProcessServiceClient
	http  *http.Client
	rest  *url.URL
	token string
}

// Dial connects to a daemon and waits until it answers health checks. With a
// tls config both addresses are the sandbox's TLS proxy and the connection is
// mutually authenticated; without one they are the daemon's plaintext ports.
func Dial(ctx context.Context, grpcAddr, restAddr, token string, tlsCfg *tls.Config) (*Client, error) {
	creds := insecure.NewCredentials()
	rest := &url.URL{Scheme: "http", Host: restAddr, Path: "/"}
	transport := http.DefaultTransport
	if tlsCfg != nil {
		creds = credentials.NewTLS(tlsCfg)
		rest.Scheme = "https"
		transport = &http.Transport{TLSClientConfig: tlsCfg, IdleConnTimeout: 30 * time.Second}
	}
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(creds), grpc.WithPerRPCCredentials(bearer(token)))
	if err != nil {
		return nil, err
	}
	c := &Client{
		conn:  conn,
		proc:  processv1.NewProcessServiceClient(conn),
		http:  &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		rest:  rest,
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

func (c *Client) Close() {
	if c != nil && c.conn != nil {
		_ = c.conn.Close()
		c.http.CloseIdleConnections()
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

// Run executes argv in the workspace and keeps up to MaxOutputBytes of each
// stream. After timeout the daemon kills the whole process group and the
// result reports exit code 124.
func (c *Client) Run(ctx context.Context, argv []string, timeout time.Duration) (Result, error) {
	stdout := &capped{limit: MaxOutputBytes}
	res, err := c.RunTo(ctx, argv, stdout, timeout)
	if err != nil {
		return Result{}, err
	}
	res.Stdout = stdout.buf.Bytes()
	res.Stderr = stdout.note(res.Stderr, "stdout")
	return res, nil
}

// RunTo is Run with stdout streamed to w as it arrives. A write error kills the
// process and fails the call.
func (c *Client) RunTo(ctx context.Context, argv []string, w io.Writer, timeout time.Duration) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.proc.Start(ctx, &processv1.StartRequest{Config: &processv1.ProcessConfig{Command: argv, Cwd: new(Workspace)}})
	if err != nil {
		return Result{}, fmt.Errorf("start %s: %w", strings.Join(argv, " "), err)
	}
	kill := func(pid int32) {
		kctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = c.proc.SendSignal(kctx, &processv1.SendSignalRequest{ProcessId: pid, Signal: processv1.Signal_SIGNAL_SIGKILL})
	}
	stderr := &capped{limit: MaxOutputBytes}
	var timedOut atomic.Bool
	var timer *time.Timer
	var pid int32
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
			pid = ev.GetInit().GetProcessId()
			if timeout > 0 {
				timer = time.AfterFunc(timeout, func() {
					timedOut.Store(true)
					kill(pid)
				})
			}
		case ev.GetExit() != nil:
			code = int(ev.GetExit().GetExitCode())
		default:
			if _, err := w.Write(ev.GetStdout()); err != nil {
				if timer != nil {
					timer.Stop()
				}
				kill(pid)
				return Result{}, fmt.Errorf("run %s: %w", strings.Join(argv, " "), err)
			}
			_, _ = stderr.Write(ev.GetStderr())
		}
		if ev.GetExit() != nil {
			break
		}
	}
	if timer != nil {
		timer.Stop()
	}
	res := Result{Stderr: stderr.note(stderr.buf.String(), "stderr"), ExitCode: code}
	if timedOut.Load() {
		res.ExitCode = 124
		res.Stderr = strings.TrimSpace(res.Stderr + "\ncommand timed out after " + timeout.String())
	}
	return res, nil
}

// filesPath escapes each segment of a literal workspace path once, so a name
// such as "a%2e" reaches the daemon as that name and not as ".".
func filesPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func (c *Client) do(ctx context.Context, method, p string, query url.Values, body io.Reader, size int64) (*http.Response, error) {
	u := c.rest.JoinPath("/v1/files/", filesPath(p))
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

// List returns a directory's entries. The trailing slash matters: without it
// the daemon answers with a redirect, which this client does not follow.
func (c *Client) List(ctx context.Context, p string) ([]Entry, error) {
	res, err := c.do(ctx, http.MethodGet, p+"/", nil, nil, 0)
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
