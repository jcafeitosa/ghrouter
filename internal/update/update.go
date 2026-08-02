package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sort"
	"strings"
)

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type Fetcher interface {
	Do(*http.Request) (*http.Response, error)
}

type FileSystem interface {
	ReadFile(string) ([]byte, error)
	TempFile(string, string) (*os.File, error)
	Replace(string, string) error
	Stat(string) (os.FileInfo, error)
}

type OSFileSystem struct{}

func (OSFileSystem) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (OSFileSystem) TempFile(dir, pattern string) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}
func (OSFileSystem) Replace(oldpath, newpath string) error { return os.Rename(newpath, oldpath) }
func (OSFileSystem) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

type Client struct {
	Repo       string
	APBaseURL   string
	HTTP       Fetcher
	FS         FileSystem
	Version    string
	GOOS       string
	GOARCH     string
}

type Result struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	AssetName       string `json:"asset_name,omitempty"`
	AssetURL        string `json:"asset_url,omitempty"`
	Applied         bool   `json:"applied"`
	TargetPath      string `json:"target_path,omitempty"`
}

func NewClient(repo, baseURL, version string, httpClient Fetcher, fs FileSystem) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		Repo:     repo,
		APBaseURL: baseURL,
		HTTP:     httpClient,
		FS:       fs,
		Version:  version,
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
	}
}

func (c *Client) LatestRelease(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APBaseURL, "/"), c.Repo), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ghrouter-update-check")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("latest release request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (c *Client) Check(ctx context.Context) (*Result, error) {
	rel, err := c.LatestRelease(ctx)
	if err != nil {
		return nil, err
	}
	res := &Result{CurrentVersion: c.Version, LatestVersion: rel.TagName, UpdateAvailable: versionGreater(rel.TagName, c.Version)}
	if asset := c.selectAsset(rel.Assets); asset != nil {
		res.AssetName = asset.Name
		res.AssetURL = asset.BrowserDownloadURL
	}
	return res, nil
}

func (c *Client) Apply(ctx context.Context, targetPath string) (*Result, error) {
	if c.FS == nil {
		c.FS = OSFileSystem{}
	}
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	check, err := c.Check(ctx)
	if err != nil {
		return nil, err
	}
	if !check.UpdateAvailable {
		return check, nil
	}
	if check.AssetURL == "" {
		return check, fmt.Errorf("no compatible release asset found for %s/%s", c.GOOS, c.GOARCH)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.AssetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("asset download failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	tmp, err := c.FS.TempFile(filepath.Dir(targetPath), "ghrouter-update-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, err
	}
	if err := c.FS.Replace(targetPath, tmpPath); err != nil {
		os.Remove(tmpPath)
		return nil, err
	}
	check.Applied = true
	check.TargetPath = targetPath
	return check, nil
}

func (c *Client) selectAsset(assets []Asset) *Asset {
	if len(assets) == 0 {
		return nil
	}
	sorted := append([]Asset(nil), assets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	want := []string{c.GOOS, c.GOARCH}
	for _, asset := range sorted {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, want[0]) && strings.Contains(name, want[1]) {
			return &asset
		}
	}
	return nil
}

func versionGreater(latest, current string) bool {
	la := parseVersion(latest)
	cu := parseVersion(current)
	for i := 0; i < len(la) && i < len(cu); i++ {
		if la[i] > cu[i] {
			return true
		}
		if la[i] < cu[i] {
			return false
		}
	}
	return len(la) > len(cu)
}

func parseVersion(raw string) []int {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, _ := strconv.Atoi(trimNonDigits(part))
		out = append(out, n)
	}
	return out
}

func trimNonDigits(raw string) string {
	for i, r := range raw {
		if r < '0' || r > '9' {
			return raw[:i]
		}
	}
	return raw
}
