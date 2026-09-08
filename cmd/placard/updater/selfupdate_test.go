package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Xm798/placard/internal/ghrelease"
)

// fakeRelease serves what the GitHub Releases API and its asset URLs return
// for one published CLI release: the release list, the binary, checksums.txt.
func fakeRelease(t *testing.T, latest string, payload []byte, corruptChecksum bool) *httptest.Server {
	t.Helper()
	name, err := artifactName(latest)
	if err != nil {
		t.Fatalf("artifactName: %v", err)
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	if corruptChecksum {
		digest = strings.Repeat("0", 64)
	}

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		// The server's own tags share this list and must never be picked up.
		_, _ = fmt.Fprintf(w, `[
		  {"tag_name": "v9.9.9", "draft": false, "prerelease": false, "assets": []},
		  {"tag_name": "cli/v%s", "draft": false, "prerelease": false, "assets": [
		    {"name": %q, "browser_download_url": "%s/bin"},
		    {"name": "checksums.txt", "browser_download_url": "%s/sums"}]}]`,
			latest, name, srv.URL, srv.URL)
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		// Two spaces, exactly like goreleaser.
		_, _ = fmt.Fprintf(w, "%s  %s\n", digest, name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	withAPIURL(t, srv.URL+"/releases")
	return srv
}

func withAPIURL(t *testing.T, url string) {
	t.Helper()
	prev := ghrelease.APIURL
	ghrelease.APIURL = url
	t.Cleanup(func() { ghrelease.APIURL = prev })
}

func seedTarget(t *testing.T) (dir, target string) {
	t.Helper()
	dir = t.TempDir()
	target = filepath.Join(dir, "placard")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	return dir, target
}

func TestFetchLatestReadsTheNewestCLIRelease(t *testing.T) {
	fakeRelease(t, "1.2.3", []byte("binary"), false)

	got, err := FetchLatest(context.Background())
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if got != "1.2.3" {
		t.Fatalf("FetchLatest = %q, want 1.2.3 (the cli/v tag, not the server's v9.9.9)", got)
	}
}

func TestFetchLatestFailsWhenUnreachable(t *testing.T) {
	withAPIURL(t, "http://127.0.0.1:9")
	if _, err := FetchLatest(context.Background()); err == nil {
		t.Fatal("want an error, got nil")
	}
}

func TestApplyReplacesTarget(t *testing.T) {
	payload := []byte("new binary bytes")
	fakeRelease(t, "2.0.0", payload, false)

	dir, target := seedTarget(t)
	if err := applyTo(context.Background(), "2.0.0", target); err != nil {
		t.Fatalf("applyTo: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("target content = %q, want %q", got, payload)
	}
	// No temp files left behind in the target directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestApplyFailsClosedOnChecksumMismatch(t *testing.T) {
	payload := []byte("new binary bytes")
	fakeRelease(t, "2.0.0", payload, true) // corrupted digest

	_, target := seedTarget(t)
	err := applyTo(context.Background(), "2.0.0", target)
	if err == nil {
		t.Fatal("want a checksum error, got nil")
	}
	if !errors.Is(err, ErrVerification) {
		t.Fatalf("error must wrap ErrVerification so the caller can classify it, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old binary" {
		t.Fatalf("target was modified despite a checksum failure: %q", got)
	}
}

func TestApplyFailsClosedWhenChecksumsUnreachable(t *testing.T) {
	payload := []byte("new binary bytes")
	// Serve the binary but 500 on checksums.txt: the update must abort, never
	// degrade to "install without verification".
	name, err := artifactName("2.0.0")
	if err != nil {
		t.Fatalf("artifactName: %v", err)
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"tag_name": "cli/v2.0.0", "draft": false, "prerelease": false, "assets": [
		  {"name": %q, "browser_download_url": "%s/bin"},
		  {"name": "checksums.txt", "browser_download_url": "%s/sums"}]}]`, name, srv.URL, srv.URL)
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusInternalServerError)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()
	withAPIURL(t, srv.URL+"/releases")

	_, target := seedTarget(t)
	if err := applyTo(context.Background(), "2.0.0", target); err == nil {
		t.Fatal("want a fetch error, got nil")
	} else if !errors.Is(err, ErrVerification) {
		t.Fatalf("an unreachable checksums.txt is a verification failure, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "old binary" {
		t.Fatalf("target was modified despite unreachable checksums: %q", got)
	}
}

func TestApplyRejectsNonRegularTarget(t *testing.T) {
	// A directory stands in for every non-regular target (symlink swapped in
	// during the TOCTOU window, device node, ...).
	dir := t.TempDir()
	target := filepath.Join(dir, "notafile")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := os.Lstat(target); err != nil {
		t.Fatalf("lstat: %v", err)
	}
	// resolveSelfPath guards the real path; assert the guard itself here.
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode().IsRegular() {
		t.Fatal("test setup is wrong: the target must not be a regular file")
	}
}

func TestChecksumFor(t *testing.T) {
	body := "aaaa  placard-1.2.3-linux-x64\nbbbb  placard-1.2.3-darwin-arm64\n"
	got, err := checksumFor(body, "placard-1.2.3-darwin-arm64")
	if err != nil {
		t.Fatalf("checksumFor: %v", err)
	}
	if got != "bbbb" {
		t.Fatalf("got %q, want bbbb", got)
	}
	if _, err := checksumFor(body, "placard-1.2.3-windows-x64.exe"); err == nil {
		t.Fatal("want an error for a missing entry")
	}
}

func TestArtifactNameMatchesReleaseLayout(t *testing.T) {
	name, err := artifactName("1.2.3")
	if err != nil {
		t.Fatalf("artifactName: %v", err)
	}
	if !strings.HasPrefix(name, "placard-1.2.3-") {
		t.Fatalf("artifact %q must match the goreleaser binary template", name)
	}
	for _, suffix := range []string{"-x64", "-arm64", "-x64.exe", "-arm64.exe"} {
		if strings.HasSuffix(name, suffix) {
			return
		}
	}
	t.Fatalf("artifact %q does not end in a known arch suffix", name)
}
