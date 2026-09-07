package injector

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

var sealedPattern = regexp.MustCompile(`brk1_[A-Za-z0-9_-]+`)

type Server struct {
	PublicKey  string
	PrivateKey string

	// UpstreamScheme is https in production. Tests point it at http.
	UpstreamScheme string
}

func (s *Server) scheme() string {
	if s.UpstreamScheme != "" {
		return s.UpstreamScheme
	}
	return "https"
}

func clientAuthToken(r *http.Request) string {
	for _, header := range []string{"Proxy-Authorization", "X-Brickie-Auth"} {
		value := r.Header.Get(header)
		if value == "" {
			continue
		}
		if token, ok := strings.CutPrefix(value, "Bearer "); ok {
			return token
		}
		if b64, ok := strings.CutPrefix(value, "Basic "); ok {
			if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
				if _, pass, found := strings.Cut(string(raw), ":"); found {
					return pass
				}
			}
		}
		return value
	}
	return ""
}

// upstreamTarget resolves where the request goes: absolute-URI requests (the
// service used as a plain HTTP forward proxy) or the /t/<host>/<path> form
// (base-URL override for SDKs that cannot speak proxy protocol).
func upstreamTarget(r *http.Request) (*url.URL, error) {
	if r.URL.IsAbs() {
		return &url.URL{Scheme: "https", Host: r.URL.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}, nil
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/t/"); ok {
		host, path, _ := strings.Cut(rest, "/")
		if host == "" {
			return nil, fmt.Errorf("missing upstream host in /t/ path")
		}
		return &url.URL{Scheme: "https", Host: host, Path: "/" + path, RawQuery: r.URL.RawQuery}, nil
	}
	return nil, fmt.Errorf("request must be an absolute proxy URI or use the /t/<host>/<path> form")
}

func (s *Server) swap(value, host, authToken string) (string, error) {
	var swapErr error
	replaced := sealedPattern.ReplaceAllStringFunc(value, func(blob string) string {
		secret, err := Open(s.PublicKey, s.PrivateKey, blob)
		if err != nil {
			swapErr = err
			return blob
		}
		if !secret.HostAllowed(host) {
			swapErr = fmt.Errorf("sealed secret is not allowed for host %s", host)
			return blob
		}
		if !secret.AuthOK(authToken) {
			swapErr = fmt.Errorf("client is not authorized to use this sealed secret")
			return blob
		}
		return secret.Token
	})
	return replaced, swapErr
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		http.Error(w, "brickie injector speaks plain HTTP only; use ANTHROPIC_BASE_URL-style overrides or absolute proxy URIs, not CONNECT", http.StatusMethodNotAllowed)
		return
	}
	target, err := upstreamTarget(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	target.Scheme = s.scheme()
	authToken := clientAuthToken(r)

	for name, values := range r.Header {
		for i, value := range values {
			swapped, err := s.swap(value, target.Host, authToken)
			if err != nil {
				http.Error(w, err.Error(), http.StatusProxyAuthRequired)
				return
			}
			values[i] = swapped
		}
		r.Header[name] = values
	}
	if rawQuery, err := s.swap(target.RawQuery, target.Host, authToken); err != nil {
		http.Error(w, err.Error(), http.StatusProxyAuthRequired)
		return
	} else {
		target.RawQuery = rawQuery
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("X-Brickie-Auth")
			pr.Out.Header.Del("Proxy-Connection")
		},
	}
	proxy.ServeHTTP(w, r)
}
