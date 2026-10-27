// Package sandboxtls is the mutual TLS between the provider and sandboxd on
// Kubernetes: a CA the provider holds, the certificates it issues, and the
// proxy that terminates TLS beside sandboxd in every sandbox pod.
package sandboxtls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	ServerName = "sandbox.brickie"
	GRPCPort   = 9443
	RESTPort   = 8443
	HealthPort = 8081

	CertFile = "tls.crt"
	KeyFile  = "tls.key"
	CAFile   = "ca.crt"

	caLifetime   = 10 * 365 * 24 * time.Hour
	certLifetime = 30 * 24 * time.Hour
	renewBefore  = 7 * 24 * time.Hour
)

type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	CertPEM []byte
	KeyPEM  []byte
}

func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "brickie sandboxd CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(caLifetime),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return LoadCA(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("ca certificate: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("ca key: not PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca key: %w", err)
	}
	return &CA{cert: cert, key: key, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// Issue signs a certificate for one side of the connection: the proxy
// (ServerAuth, named ServerName) or the provider (ClientAuth).
func (ca *CA) Issue(usage x509.ExtKeyUsage) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "brickie provider"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if usage == x509.ExtKeyUsageServerAuth {
		tmpl.Subject.CommonName = ServerName
		tmpl.DNSNames = []string{ServerName}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// Fresh reports whether certPEM was issued by this CA and is not due for
// renewal.
func (ca *CA) Fresh(certPEM []byte) bool {
	cert, err := parseCert(certPEM)
	return err == nil && cert.CheckSignatureFrom(ca.cert) == nil && time.Until(cert.NotAfter) > renewBefore
}

// ClientConfig is how the provider dials a sandbox proxy: it trusts only this
// CA and presents a client certificate that is reissued before it expires.
func (ca *CA) ClientConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	var mu sync.Mutex
	var cur *tls.Certificate
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: ServerName,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			mu.Lock()
			defer mu.Unlock()
			if cur == nil || time.Until(cur.Leaf.NotAfter) < renewBefore {
				certPEM, keyPEM, err := ca.Issue(x509.ExtKeyUsageClientAuth)
				if err != nil {
					return nil, err
				}
				c, err := tls.X509KeyPair(certPEM, keyPEM)
				if err != nil {
					return nil, err
				}
				cur = &c
			}
			return cur, nil
		},
	}
}

// Serve is the proxy: mutual TLS on GRPCPort and RESTPort forwarded byte for
// byte to sandboxd's plaintext ports, and a plain /healthz on HealthPort that
// mirrors sandboxd's health for the readiness probe. dir holds CAFile,
// CertFile, and KeyFile as the provider writes them into the Secret; the
// certificate is read per handshake, so a renewed Secret needs no restart.
func Serve(ctx context.Context, dir, grpcTarget, restTarget string) error {
	caPEM, err := os.ReadFile(filepath.Join(dir, CAFile)) // #nosec G304 -- the mounted Secret
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("%s: no certificate", filepath.Join(dir, CAFile))
	}
	base := &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			c, err := tls.LoadX509KeyPair(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
			return &c, err
		},
	}
	for _, l := range []struct {
		port   int
		proto  string
		target string
	}{{GRPCPort, "h2", grpcTarget}, {RESTPort, "http/1.1", restTarget}} {
		cfg := base.Clone()
		cfg.NextProtos = []string{l.proto}
		ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", l.port), cfg)
		if err != nil {
			return err
		}
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		go func(target string) {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go forward(ctx, conn.(*tls.Conn), target)
			}
		}(l.target)
	}
	probe := &http.Client{Timeout: 2 * time.Second}
	health := &http.Server{
		Addr:              fmt.Sprintf(":%d", HealthPort),
		ReadHeaderTimeout: 2 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+restTarget+"/v1/health", nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			res, err := probe.Do(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			_ = res.Body.Close()
			w.WriteHeader(res.StatusCode)
		}),
	}
	go func() {
		<-ctx.Done()
		_ = health.Close()
	}()
	if err := health.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func forward(ctx context.Context, conn *tls.Conn, target string) {
	defer func() { _ = conn.Close() }()
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := conn.HandshakeContext(hctx)
	cancel()
	if err != nil {
		return
	}
	up, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(up, conn)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, up)
		done <- struct{}{}
	}()
	<-done
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}
	return n
}
