package injector

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("hatchet token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode hatchet token payload: %w", err)
	}
	claims := map[string]any{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// HatchetUpstreams extracts the engine endpoints from a real Hatchet token.
func HatchetUpstreams(token string) (grpcAddr, serverURL string, err error) {
	claims, err := jwtClaims(token)
	if err != nil {
		return "", "", err
	}
	grpcAddr, _ = claims["grpc_broadcast_address"].(string)
	serverURL, _ = claims["server_url"].(string)
	if grpcAddr == "" {
		return "", "", fmt.Errorf("hatchet token has no grpc_broadcast_address claim")
	}
	return grpcAddr, serverURL, nil
}

// MintHatchetPlaceholder builds a JWT with the real token's claims but with
// endpoints rewritten to the sidecar and a random signature. Hatchet SDKs
// parse it for configuration; the engine rejects it; leaking it leaks
// nothing. The claims of the real token are not confidential, only its
// signature is.
func MintHatchetPlaceholder(realToken, listenHostPort string) (string, error) {
	claims, err := jwtClaims(realToken)
	if err != nil {
		return "", err
	}
	claims["grpc_broadcast_address"] = listenHostPort
	claims["server_url"] = "http://" + listenHostPort
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	sig := make([]byte, 32)
	if _, err := rand.Read(sig); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// HatchetProxy terminates the sandbox's plaintext Hatchet connections (h2c
// for gRPC, HTTP/1 for the REST API), swaps in the real token, and dials the
// engine. The real token lives only in the sidecar's memory.
type HatchetProxy struct {
	Token         string
	GRPCUpstream  string
	APIUpstream   string
	PlainUpstream bool
}

func NewHatchetProxyFromToken(token string, plainUpstream bool) (*HatchetProxy, error) {
	grpcAddr, serverURL, err := HatchetUpstreams(token)
	if err != nil {
		return nil, err
	}
	return &HatchetProxy{Token: token, GRPCUpstream: grpcAddr, APIUpstream: serverURL, PlainUpstream: plainUpstream}, nil
}

func (p *HatchetProxy) upstreamScheme() string {
	if p.PlainUpstream {
		return "http"
	}
	return "https"
}

func (p *HatchetProxy) grpcTransport() http.RoundTripper {
	var protocols http.Protocols
	if p.PlainUpstream {
		protocols.SetUnencryptedHTTP2(true)
	} else {
		protocols.SetHTTP2(true)
	}
	return &http.Transport{Protocols: &protocols}
}

func (p *HatchetProxy) Handler() http.Handler {
	grpcProxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = p.upstreamScheme()
			pr.Out.URL.Host = p.GRPCUpstream
			pr.Out.Host = p.GRPCUpstream
			pr.Out.Header.Set("Authorization", "Bearer "+p.Token)
		},
		Transport:     p.grpcTransport(),
		FlushInterval: -1,
	}
	var apiProxy http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "hatchet api upstream is not configured", http.StatusBadGateway)
	})
	if p.APIUpstream != "" {
		if apiURL, err := url.Parse(p.APIUpstream); err == nil {
			apiProxy = &httputil.ReverseProxy{
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.Out.URL.Scheme = apiURL.Scheme
					pr.Out.URL.Host = apiURL.Host
					pr.Out.Host = apiURL.Host
					pr.Out.Header.Set("Authorization", "Bearer "+p.Token)
				},
			}
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcProxy.ServeHTTP(w, r)
			return
		}
		apiProxy.ServeHTTP(w, r)
	})
}

// Serve accepts HTTP/1 and unencrypted HTTP/2 (gRPC from the sandbox) on the
// same listener using the standard library's h2c support.
func (p *HatchetProxy) Serve(listen string) error {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{Addr: listen, Handler: p.Handler(), Protocols: &protocols}
	return server.ListenAndServe()
}
