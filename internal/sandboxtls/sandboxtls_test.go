package sandboxtls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

func TestProxyAdmitsOnlyTheProvider(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.Issue(x509.ExtKeyUsageServerAuth)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range map[string][]byte{CAFile: ca.CertPEM, CertFile: certPEM, KeyFile: keyPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
		case "/v1/files/x":
			_, _ = w.Write([]byte("forwarded"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer rest.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	healthv1.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(ln) }()
	defer srv.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, dir, ln.Addr().String(), rest.Listener.Addr().String()) }()
	for i := 0; ; i++ {
		res, err := http.Get("http://127.0.0.1:8081/healthz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("healthz: HTTP %d", res.StatusCode)
			}
			break
		}
		if i == 50 {
			t.Fatalf("proxy did not come up: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	provider := &http.Client{Transport: &http.Transport{TLSClientConfig: ca.ClientConfig()}}
	res, err := provider.Get("https://127.0.0.1:8443/v1/files/x")
	if err != nil {
		t.Fatalf("provider over REST: %v", err)
	}
	body := make([]byte, 32)
	n, _ := res.Body.Read(body)
	_ = res.Body.Close()
	if got := string(body[:n]); res.StatusCode != http.StatusOK || got != "forwarded" {
		t.Fatalf("REST: HTTP %d %q", res.StatusCode, got)
	}

	conn, err := grpc.NewClient("127.0.0.1:9443", grpc.WithTransportCredentials(credentials.NewTLS(ca.ClientConfig())))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	check, err := healthv1.NewHealthClient(conn).Check(ctx, &healthv1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("provider over gRPC: %v", err)
	}
	if check.GetStatus() != healthv1.HealthCheckResponse_SERVING {
		t.Fatalf("gRPC health %s", check.GetStatus())
	}

	other, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM)
	for name, cfg := range map[string]*tls.Config{
		"no client certificate": {MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: ServerName},
		"certificate from another CA": func() *tls.Config {
			c := other.ClientConfig()
			c.RootCAs = pool
			return c
		}(),
	} {
		stranger := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		if res, err := stranger.Get("https://127.0.0.1:8443/v1/files/x"); err == nil {
			_ = res.Body.Close()
			t.Fatalf("%s was admitted", name)
		}
	}
}
