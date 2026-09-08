package ghrelease

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// releasesJSON is the shape GitHub returns: both tag namespaces in one list,
// newest first.
const releasesJSON = `[
  {"tag_name": "v1.2.3", "draft": false, "prerelease": false,
   "assets": [{"name": "placard-server-1.2.3-linux-x64", "browser_download_url": "https://example.test/server"}]},
  {"tag_name": "cli/v1.0.0-rc.1", "draft": false, "prerelease": true,
   "assets": [{"name": "placard-1.0.0-rc.1-linux-x64", "browser_download_url": "https://example.test/rc"}]},
  {"tag_name": "cli/v0.9.0", "draft": false, "prerelease": false,
   "assets": [
     {"name": "placard-0.9.0-linux-x64", "browser_download_url": "https://example.test/bin"},
     {"name": "checksums.txt", "browser_download_url": "https://example.test/sums"}]},
  {"tag_name": "cli/v0.8.0", "draft": true, "prerelease": false, "assets": []},
  {"tag_name": "cli/v0.7.0", "draft": false, "prerelease": false, "assets": []}
]`

func fakeAPI(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			// GitHub itself answers 403 without one; fail loudly rather than
			// letting a UA-less request pass in tests only.
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := APIURL
	APIURL = srv.URL
	t.Cleanup(func() { APIURL = prev })
	return srv
}

func TestLatestPicksTheNewestCLIRelease(t *testing.T) {
	fakeAPI(t, releasesJSON)

	got, err := Latest(context.Background(), http.DefaultClient)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	// v1.2.3 is the SERVER's tag and sorts first; cli/v1.0.0-rc.1 is a
	// prerelease. Neither may be handed to a CLI.
	if got.Version != "0.9.0" {
		t.Fatalf("Latest = %q, want 0.9.0", got.Version)
	}
	if got.Assets["checksums.txt"] != "https://example.test/sums" {
		t.Errorf("assets = %v, want the URLs GitHub reported", got.Assets)
	}
}

func TestListDropsDraftsAndOtherTagNamespaces(t *testing.T) {
	fakeAPI(t, releasesJSON)

	got, full, err := List(context.Background(), http.DefaultClient, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if full {
		t.Error("a short page is the last one")
	}
	want := []string{"1.0.0-rc.1", "0.9.0", "0.7.0"}
	if len(got) != len(want) {
		t.Fatalf("List returned %d releases (%v), want %v", len(got), got, want)
	}
	for i, v := range want {
		if got[i].Version != v {
			t.Errorf("release %d = %q, want %q", i, got[i].Version, v)
		}
	}
}

func TestFindMatchesAnExactVersionIncludingPrereleases(t *testing.T) {
	fakeAPI(t, releasesJSON)

	got, err := Find(context.Background(), http.DefaultClient, "1.0.0-rc.1")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got.Assets["placard-1.0.0-rc.1-linux-x64"] != "https://example.test/rc" {
		t.Errorf("assets = %v", got.Assets)
	}
	if _, err := Find(context.Background(), http.DefaultClient, "9.9.9"); err == nil {
		t.Error("want an error for a version that was never released")
	}
}

func TestLatestFailsWhenNoCLIReleaseExists(t *testing.T) {
	fakeAPI(t, `[{"tag_name": "v1.2.3", "draft": false, "prerelease": false, "assets": []}]`)

	if _, err := Latest(context.Background(), http.DefaultClient); err == nil {
		t.Fatal("want an error when the repository has only server releases")
	}
}

func TestListReportsHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	prev := APIURL
	APIURL = srv.URL
	t.Cleanup(func() { APIURL = prev })

	if _, _, err := List(context.Background(), http.DefaultClient, 1); err == nil {
		t.Fatal("want an error for a non-200 response")
	}
}

// Server and CLI releases share one list, so a run of server releases can push
// the newest CLI release past the first page. Stopping there would report that
// the CLI has no releases at all.
func TestLatestWalksPastAFullPageOfServerReleases(t *testing.T) {
	var page1 []string
	for i := 0; i < perPage; i++ {
		page1 = append(page1, fmt.Sprintf(`{"tag_name": "v1.0.%d", "draft": false, "prerelease": false, "assets": []}`, i))
	}
	pages := map[string]string{
		"1": "[" + strings.Join(page1, ",") + "]",
		"2": `[{"tag_name": "cli/v3.1.0", "draft": false, "prerelease": false, "assets": []}]`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Query().Get("page")]
		if !ok {
			body = "[]"
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	prev := APIURL
	APIURL = srv.URL
	t.Cleanup(func() { APIURL = prev })

	got, err := Latest(context.Background(), http.DefaultClient)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got.Version != "3.1.0" {
		t.Fatalf("Latest = %q, want 3.1.0 from the second page", got.Version)
	}
}
