package injector

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeHatchetToken(t *testing.T, grpcAddr, serverURL string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"sub":                    "tenant-123",
		"grpc_broadcast_address": grpcAddr,
		"server_url":             serverURL,
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("real-signature"))
}

func TestMintHatchetPlaceholder(t *testing.T) {
	real := fakeHatchetToken(t, "engine.example.com:443", "https://engine.example.com")
	placeholder, err := MintHatchetPlaceholder(real, "127.0.0.1:7077")
	if err != nil {
		t.Fatal(err)
	}
	if placeholder == real {
		t.Fatal("placeholder must differ from the real token")
	}
	if strings.Split(placeholder, ".")[2] == strings.Split(real, ".")[2] {
		t.Fatal("placeholder must not carry the real signature")
	}
	grpcAddr, serverURL, err := HatchetUpstreams(placeholder)
	if err != nil {
		t.Fatal(err)
	}
	if grpcAddr != "127.0.0.1:7077" || serverURL != "http://127.0.0.1:7077" {
		t.Fatalf("placeholder endpoints wrong: %s %s", grpcAddr, serverURL)
	}
	claims, _ := jwtClaims(placeholder)
	if claims["sub"] != "tenant-123" {
		t.Fatal("placeholder must preserve non-endpoint claims")
	}
}

func TestHatchetProxySwapsAuthOnAPIRequests(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, "api-ok")
	}))
	defer upstream.Close()

	real := fakeHatchetToken(t, "unused:443", upstream.URL)
	proxy, err := NewHatchetProxyFromToken(real, true)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(proxy.Handler())
	defer front.Close()

	placeholder, _ := MintHatchetPlaceholder(real, strings.TrimPrefix(front.URL, "http://"))
	req, _ := http.NewRequest("GET", front.URL+"/api/v1/workers", nil)
	req.Header.Set("Authorization", "Bearer "+placeholder)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "api-ok" {
		t.Fatalf("proxy round trip failed: %s", body)
	}
	if gotAuth != "Bearer "+real {
		t.Fatalf("upstream did not receive the real token, got %q", gotAuth)
	}
}
