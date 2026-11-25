package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.9.5", "v0.9.6", true},
		{"v0.9.5", "v0.10.0", true},
		{"v0.9.5", "v1.0.0", true},
		{"v0.9.6", "v0.9.6", false},
		{"v0.9.6", "v0.9.5", false},
		{"v0.10.0", "v0.9.9", false},
		{"dev", "v9.9.9", false},
		{"", "v9.9.9", false},
		{"v0.9.6-0.20260101000000-abcdef123456", "v0.9.7", false},
		{"v0.9.6+dirty", "v0.9.7", false},
		{"v0.9.5", "garbage", false},
	} {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %t, want %t", c.current, c.latest, got, c.want)
		}
	}
}

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type release struct {
	tag      string
	archive  []byte
	checksum string
	latest   atomic.Int32
}

func (r *release) asset() string {
	return fmt.Sprintf("drover_%s_%s_%s.tar.gz", strings.TrimPrefix(r.tag, "v"), runtime.GOOS, runtime.GOARCH)
}

func serve(t *testing.T, r *release) *Client {
	t.Helper()
	if r.checksum == "" {
		sum := sha256.Sum256(r.archive)
		r.checksum = fmt.Sprintf("%x", sum)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		r.latest.Add(1)
		fmt.Fprintf(w, `{"tag_name":%q}`, r.tag)
	})
	mux.HandleFunc("/dl/"+r.tag+"/"+r.asset(), func(w http.ResponseWriter, _ *http.Request) { w.Write(r.archive) })
	mux.HandleFunc("/dl/"+r.tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n%s  other.tar.gz\n", r.checksum, r.asset(), strings.Repeat("0", 64))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Client{HTTP: srv.Client(), API: srv.URL + "/api", Downloads: srv.URL + "/dl"}
}

func TestInstallReplacesTheBinary(t *testing.T) {
	r := &release{tag: "v1.2.3", archive: tarball(t, map[string]string{"LICENSE": "x", "drover": "new binary"})}
	c := serve(t, r)
	exe := filepath.Join(t.TempDir(), "drover")
	os.WriteFile(exe, []byte("old binary"), 0o755)

	if err := c.Install(context.Background(), r.tag, exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new binary" {
		t.Errorf("binary = %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", info.Mode())
	}
	if entries, _ := os.ReadDir(filepath.Dir(exe)); len(entries) != 1 {
		t.Errorf("left temp files behind: %v", entries)
	}
}

func TestInstallLeavesTheBinaryAloneOnFailure(t *testing.T) {
	good := tarball(t, map[string]string{"drover": "new binary"})
	for _, c := range []struct {
		name string
		rel  *release
		want string
	}{
		{"bad checksum", &release{tag: "v1.2.3", archive: good, checksum: strings.Repeat("a", 64)}, "checksum mismatch"},
		{"no binary in archive", &release{tag: "v1.2.3", archive: tarball(t, map[string]string{"LICENSE": "x"})}, "no drover binary"},
		{"not an archive", &release{tag: "v1.2.3", archive: []byte("this is not a gzip stream")}, "gzip"},
		{"bad tag", &release{tag: "latest", archive: good}, "unexpected release tag"},
	} {
		t.Run(c.name, func(t *testing.T) {
			client := serve(t, c.rel)
			exe := filepath.Join(t.TempDir(), "drover")
			os.WriteFile(exe, []byte("old binary"), 0o755)

			err := client.Install(context.Background(), c.rel.tag, exe)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
			if got, _ := os.ReadFile(exe); string(got) != "old binary" {
				t.Errorf("binary changed to %q", got)
			}
		})
	}
}

func TestInstallFailsWhenTheDirectoryIsNotWritable(t *testing.T) {
	r := &release{tag: "v1.2.3", archive: tarball(t, map[string]string{"drover": "new binary"})}
	c := serve(t, r)
	err := c.Install(context.Background(), r.tag, filepath.Join(t.TempDir(), "missing", "drover"))
	if err == nil || !strings.Contains(err.Error(), "can't write next to") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckAsksAtMostOnceADay(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(EnvDisable, "")
	r := &release{tag: "v1.2.3"}
	c := serve(t, r)

	if tag, ok := c.Check(context.Background(), "v1.2.0"); !ok || tag != "v1.2.3" {
		t.Fatalf("Check = %q, %t", tag, ok)
	}
	if tag, ok := c.Check(context.Background(), "v1.2.0"); !ok || tag != "v1.2.3" {
		t.Fatalf("cached Check = %q, %t", tag, ok)
	}
	if n := r.latest.Load(); n != 1 {
		t.Errorf("asked GitHub %d times, want 1", n)
	}
	if _, ok := c.Check(context.Background(), "v1.2.3"); ok {
		t.Error("offered the version already installed")
	}

	stale, _ := json.Marshal(cache{CheckedAt: time.Now().Add(-2 * checkEvery), Latest: "v1.2.3"})
	os.WriteFile(cachePath(), stale, 0o644)
	c.Check(context.Background(), "v1.2.0")
	if n := r.latest.Load(); n != 2 {
		t.Errorf("asked GitHub %d times after the cache went stale, want 2", n)
	}
}

func TestCheckSkipsDevBuildsAndOptOut(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := &release{tag: "v9.9.9"}
	c := serve(t, r)

	t.Setenv(EnvDisable, "")
	if _, ok := c.Check(context.Background(), "dev"); ok {
		t.Error("a dev build was offered an update")
	}
	t.Setenv(EnvDisable, "1")
	if _, ok := c.Check(context.Background(), "v1.0.0"); ok {
		t.Error("the opt-out was ignored")
	}
	if n := r.latest.Load(); n != 0 {
		t.Errorf("asked GitHub %d times, want 0", n)
	}
}

func TestCheckIsQuietWhenOffline(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(EnvDisable, "")
	c := &Client{API: "http://127.0.0.1:1"}
	if _, ok := c.Check(context.Background(), "v1.0.0"); ok {
		t.Error("offered an update with no network")
	}
}

func TestInstallRejectsUnusableReleases(t *testing.T) {
	good := tarball(t, map[string]string{"drover": "new binary"})
	r := &release{tag: "v1.2.3", archive: good}
	c := serve(t, r)
	exe := filepath.Join(t.TempDir(), "drover")
	os.WriteFile(exe, []byte("old binary"), 0o755)

	// No assets are published for this tag.
	if err := c.Install(context.Background(), "v1.2.4", exe); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing assets: err = %v", err)
	}
	if err := verify(good, []byte(strings.Repeat("0", 64)+"  other.tar.gz\n"), r.asset()); err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Errorf("asset missing from checksums: err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Errorf("binary changed to %q", got)
	}
}

func TestCheckIgnoresACacheFromTheFuture(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(EnvDisable, "")
	r := &release{tag: "v1.2.3"}
	c := serve(t, r)
	future, _ := json.Marshal(cache{CheckedAt: time.Now().Add(48 * time.Hour), Latest: "v1.0.1"})
	os.MkdirAll(filepath.Dir(cachePath()), 0o755)
	os.WriteFile(cachePath(), future, 0o644)

	if tag, ok := c.Check(context.Background(), "v1.0.0"); !ok || tag != "v1.2.3" {
		t.Errorf("Check = %q, %t, want the fresh v1.2.3", tag, ok)
	}
}
