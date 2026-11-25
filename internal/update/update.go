// Package update finds newer drover releases on GitHub and installs them over
// the running binary.
package update

import (
	"archive/tar"
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

	"github.com/aapzu/drover/internal/store"
)

const (
	EnvDisable = "DROVER_NO_UPDATE_CHECK"

	checkEvery  = 24 * time.Hour
	maxDownload = 128 << 20
)

// Client talks to a GitHub-shaped release host. The zero value uses github.com.
type Client struct {
	HTTP           *http.Client
	API, Downloads string
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) api() string {
	if c.API != "" {
		return c.API
	}
	return "https://api.github.com/repos/aapzu/drover"
}

func (c *Client) downloads() string {
	if c.Downloads != "" {
		return c.Downloads
	}
	return "https://github.com/aapzu/drover/releases/download"
}

func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "drover-update")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest returns the tag of the newest release.
func (c *Client) Latest(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, c.api()+"/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var rel struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return "", err
	}
	if _, ok := parse(rel.Tag); !ok {
		return "", fmt.Errorf("unexpected release tag %q", rel.Tag)
	}
	return rel.Tag, nil
}

var versionRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

func parse(v string) ([3]int, bool) {
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		out[i], _ = strconv.Atoi(m[i+1])
	}
	return out, true
}

// Newer reports whether latest is a later release than current. Anything that
// isn't a plain vX.Y.Z, such as a "dev" or pseudo-version build, never is.
func Newer(current, latest string) bool {
	c, ok := parse(current)
	if !ok {
		return false
	}
	l, ok := parse(latest)
	if !ok {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// Releasable reports whether version is a tagged release that updates apply to.
func Releasable(version string) bool {
	_, ok := parse(version)
	return ok
}

type cache struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
}

func cachePath() string { return filepath.Join(store.Home(), "update.json") }

// Check returns the newest release tag when it is newer than current. It asks
// GitHub at most once a day and stays quiet when offline.
func (c *Client) Check(ctx context.Context, current string) (string, bool) {
	if os.Getenv(EnvDisable) != "" || !Releasable(current) {
		return "", false
	}
	path := cachePath()
	var cached cache
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &cached)
	}
	latest := cached.Latest
	if age := time.Since(cached.CheckedAt); age > checkEvery || age < 0 {
		tag, err := c.Latest(ctx)
		if err != nil {
			return "", false
		}
		latest = tag
		if data, err := json.Marshal(cache{CheckedAt: time.Now(), Latest: tag}); err == nil {
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, data, 0o644)
		}
	}
	return latest, Newer(current, latest)
}

// Executable is the path of the running binary with symlinks resolved.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Install downloads the release tagged tag for this platform, checks it against
// the release's checksums and replaces the file at exe with it.
func (c *Client) Install(ctx context.Context, tag, exe string) error {
	if !Releasable(tag) {
		return fmt.Errorf("unexpected release tag %q", tag)
	}
	asset := fmt.Sprintf("drover_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	sums, err := c.fetch(ctx, tag, "checksums.txt")
	if err != nil {
		return err
	}
	archive, err := c.fetch(ctx, tag, asset)
	if err != nil {
		return err
	}
	if err := verify(archive, sums, asset); err != nil {
		return err
	}
	bin, err := extract(archive)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

func (c *Client) fetch(ctx context.Context, tag, name string) ([]byte, error) {
	resp, err := c.get(ctx, c.downloads()+"/"+tag+"/"+name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readCapped(resp.Body, name)
}

func readCapped(r io.Reader, what string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("%s is larger than %d MB", what, maxDownload>>20)
	}
	return data, nil
}

func verify(archive, sums []byte, asset string) error {
	sum := sha256.Sum256(archive)
	got := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			if !strings.EqualFold(fields[0], got) {
				return fmt.Errorf("checksum mismatch for %s", asset)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum for %s", asset)
}

func extract(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no drover binary in the release archive")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "drover" {
			continue
		}
		return readCapped(tr, "the drover binary")
	}
}

// replace swaps in a new file next to exe, so a running process keeps its old
// inode and the path is never half-written.
func replace(exe string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".drover-update-*")
	if err != nil {
		return fmt.Errorf("can't write next to %s: %w", exe, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}
