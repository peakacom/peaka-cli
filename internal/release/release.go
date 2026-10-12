// Package release finds, downloads, verifies and installs releases of the CLI
// from GitHub (peakacom/peaka-cli), as goreleaser publishes them: one archive
// per platform named peaka_<version>_<os>_<arch>.tar.gz (zip on Windows) and
// a checksums.txt next to them.
package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const Repo = "peakacom/peaka-cli"

// Release is one GitHub release with its assets.
type Release struct {
	Tag        string
	Version    string // the tag without its v
	URL        string // the release page
	Prerelease bool
	Assets     map[string]Asset
}

// Asset is one file on a release; APIURL serves it when the repository is private.
type Asset struct {
	Name   string
	URL    string
	APIURL string
	Size   int64
}

// Client talks to the GitHub API for one repository.
type Client struct {
	Repo  string
	Token string // GITHUB_TOKEN / GH_TOKEN: needed only while the repository is private
	HTTP  *http.Client
}

func New() *Client {
	return &Client{Repo: Repo, Token: Token(), HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Token is the GitHub token from the environment, if any.
func Token() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Latest is the newest release that is not a prerelease.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	return c.get(ctx, "https://api.github.com/repos/"+c.Repo+"/releases/latest")
}

// ByTag is one release by its tag (v1.2.3; a bare 1.2.3 is accepted).
func (c *Client) ByTag(ctx context.Context, tag string) (*Release, error) {
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return c.get(ctx, "https://api.github.com/repos/"+c.Repo+"/releases/tags/"+tag)
}

func (c *Client) get(ctx context.Context, url string) (*Release, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("no such release at github.com/%s (a private repository needs GITHUB_TOKEN)", c.Repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("github refused (%d): rate limited or not allowed; set GITHUB_TOKEN", resp.StatusCode)
	default:
		return nil, fmt.Errorf("github answered HTTP %d", resp.StatusCode)
	}
	var raw struct {
		Tag        string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			APIURL string `json:"url"`
			Size   int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.Tag == "" {
		return nil, errors.New("github: unexpected release document")
	}
	r := &Release{Tag: raw.Tag, Version: strings.TrimPrefix(raw.Tag, "v"), URL: raw.HTMLURL, Prerelease: raw.Prerelease, Assets: map[string]Asset{}}
	for _, a := range raw.Assets {
		r.Assets[a.Name] = Asset{Name: a.Name, URL: a.URL, APIURL: a.APIURL, Size: a.Size}
	}
	return r, nil
}

// ArchiveName is the asset this platform installs from.
func ArchiveName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("peaka_%s_%s_%s.%s", version, goos, goarch, ext)
}

// Download fetches one asset, through the API when a token is set (the only
// way into a private repository's assets).
func (c *Client) Download(ctx context.Context, a Asset) ([]byte, error) {
	url := a.URL
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if c.Token != "" && a.APIURL != "" {
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, a.APIURL, nil)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/octet-stream")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", a.Name, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}

// Checksums parses checksums.txt: "<sha256>  <file>" per line.
func Checksums(data []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = f[0]
		}
	}
	return out
}

// Verify checks an archive against its published checksum.
func Verify(name string, data []byte, sums map[string]string) error {
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("checksums.txt does not list %s", name)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum of %s does not match (got %s, want %s)", name, got[:12], want[:12])
	}
	return nil
}

// Binary extracts the peaka executable from an archive.
func Binary(name string, data []byte) ([]byte, error) {
	want := "peaka"
	if strings.HasSuffix(name, ".zip") {
		want += ".exe"
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == want {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("%s holds no %s", name, want)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == want {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("%s holds no %s", name, want)
}

// Install puts the new executable where the running one is, atomically where
// the OS allows: written next to it, then renamed over it. On Windows the
// running file is moved aside first (it cannot be overwritten while it runs).
func Install(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".peaka-upgrade-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", exe, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmpName)
			return fmt.Errorf("cannot move the running executable aside: %w", err)
		}
	}
	if err := os.Rename(tmpName, exe); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	return nil
}

// Executable is the running binary's real path (symlinks resolved).
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return exe, nil
}

// Managed says which package manager owns the executable, if one does.
func Managed(exe string) string {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/Cellar/") || strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return "brew"
	case strings.HasPrefix(p, "/usr/bin/") && runtime.GOOS == "linux":
		return "package" // deb/rpm
	}
	return ""
}

var releaseVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$`)

// IsRelease says whether a version string is a release (1.2.3, v1.2.3,
// 1.2.3-rc1), as opposed to "dev" or a git describe from a working tree.
func IsRelease(v string) bool {
	m := releaseVersion.FindStringSubmatch(v)
	return m != nil && !strings.Contains(m[4], "-g") && !strings.Contains(v, "dirty") && !strings.HasSuffix(v, "-next")
}

// Compare orders two versions: negative when a < b. Prerelease suffixes sort
// before the release ("1.2.0-rc1" < "1.2.0"); anything unparsable is oldest.
func Compare(a, b string) int {
	ma, mb := releaseVersion.FindStringSubmatch(a), releaseVersion.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		switch {
		case ma == nil && mb == nil:
			return strings.Compare(a, b)
		case ma == nil:
			return -1
		default:
			return 1
		}
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case ma[4] == mb[4]:
		return 0
	case ma[4] == "":
		return 1
	case mb[4] == "":
		return -1
	}
	return strings.Compare(ma[4], mb[4])
}
