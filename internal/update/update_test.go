package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDetectsLatestRelease(t *testing.T) {
	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/jcafeitosa/ghrouter/releases/latest" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"tag_name":"v9.9.9","assets":[{"name":"ghrouter_darwin_arm64","browser_download_url":"`+baseURL+`/asset"}]}`)
	}))
	defer srv.Close()
	baseURL = srv.URL

	client := NewClient("jcafeitosa/ghrouter", srv.URL, "v1.0.0", srv.Client(), OSFileSystem{})
	client.GOOS = "darwin"
	client.GOARCH = "arm64"
	res, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !res.UpdateAvailable {
		t.Fatalf("expected update available, got %+v", res)
	}
	if res.AssetName == "" {
		t.Fatalf("expected asset name, got %+v", res)
	}
}

func TestApplyDownloadsAndReplacesTarget(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "ghrouter")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	var baseURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/jcafeitosa/ghrouter/releases/latest":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"tag_name":"v2.0.0","assets":[{"name":"ghrouter_darwin_arm64","browser_download_url":"`+baseURL+`/asset"}]}`)
		case "/asset":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "new-binary")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	baseURL = srv.URL

	client := NewClient("jcafeitosa/ghrouter", srv.URL, "v1.0.0", srv.Client(), OSFileSystem{})
	client.GOOS = "darwin"
	client.GOARCH = "arm64"
	res, err := client.Apply(context.Background(), target)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Applied {
		t.Fatalf("expected applied result, got %+v", res)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(data, []byte("new-binary")) {
		t.Fatalf("unexpected target contents: %q", string(data))
	}
}
