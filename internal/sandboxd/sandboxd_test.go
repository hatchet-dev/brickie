package sandboxd

import (
	"net/url"
	"testing"
)

func TestFilesPathEscapesLiteralNamesOnce(t *testing.T) {
	rest := &url.URL{Scheme: "http", Host: "sandbox:8080", Path: "/"}
	for in, want := range map[string]string{
		"a b.txt":       "/v1/files/a%20b.txt",
		"a%20b.txt":     "/v1/files/a%2520b.txt",
		"%2e":           "/v1/files/%252e",
		"dir/x%2fy":     "/v1/files/dir/x%252fy",
		"notes/é.md":    "/v1/files/notes/%C3%A9.md",
		"plain/name.go": "/v1/files/plain/name.go",
	} {
		if got := rest.JoinPath("/v1/files/", filesPath(in)).RequestURI(); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
}
