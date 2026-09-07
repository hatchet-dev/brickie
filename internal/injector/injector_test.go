package injector

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSealSwapRoundTrip(t *testing.T) {
	pub, priv, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}

	var gotAuth, gotQuery, gotProxyAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Key")
		gotQuery = r.URL.Query().Get("key")
		gotProxyAuth = r.Header.Get("Proxy-Authorization")
		io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()
	upstreamHost := strings.TrimPrefix(upstream.URL, "http://")

	sealed, err := Seal(pub, Secret{
		Token:        "real-secret-token",
		AllowedHosts: []string{strings.Split(upstreamHost, ":")[0]},
		AuthDigest:   AuthDigest("sandbox-auth"),
	})
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(&Server{PublicKey: pub, PrivateKey: priv, UpstreamScheme: "http"})
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL+"/t/"+upstreamHost+"/v1/messages?key="+url.QueryEscape(sealed), nil)
	req.Header.Set("X-Api-Key", sealed)
	req.Header.Set("Proxy-Authorization", "Bearer sandbox-auth")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != 200 || string(body) != "upstream-ok" {
		t.Fatalf("proxy round trip failed: %d %s", res.StatusCode, body)
	}
	if gotAuth != "real-secret-token" {
		t.Fatalf("header swap failed, upstream saw %q", gotAuth)
	}
	if gotQuery != "real-secret-token" {
		t.Fatalf("query swap failed, upstream saw %q", gotQuery)
	}
	if gotProxyAuth != "" {
		t.Fatalf("proxy auth leaked upstream: %q", gotProxyAuth)
	}

	req2, _ := http.NewRequest("GET", server.URL+"/t/"+upstreamHost+"/v1/messages", nil)
	req2.Header.Set("X-Api-Key", sealed)
	req2.Header.Set("Proxy-Authorization", "Bearer wrong-token")
	res2, _ := http.DefaultClient.Do(req2)
	res2.Body.Close()
	if res2.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407 for wrong client auth, got %d", res2.StatusCode)
	}

	sealedOther, _ := Seal(pub, Secret{Token: "x", AllowedHosts: []string{"api.other.com"}})
	req3, _ := http.NewRequest("GET", server.URL+"/t/"+upstreamHost+"/v1", nil)
	req3.Header.Set("X-Api-Key", sealedOther)
	res3, _ := http.DefaultClient.Do(req3)
	res3.Body.Close()
	if res3.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407 for disallowed host, got %d", res3.StatusCode)
	}

	if _, err := Open(pub, priv, "brk1_not-a-real-blob"); err == nil {
		t.Fatal("expected malformed blob to fail to open")
	}
	otherPub, otherPriv, _ := GenerateKeypair()
	_ = otherPub
	if _, err := Open(pub, otherPriv, sealed); err == nil {
		t.Fatal("expected wrong private key to fail to open")
	}
}
