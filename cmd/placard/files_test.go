package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// pagedFilesServer serves `total` synthetic files honouring page/page_size.
func pagedFilesServer(t *testing.T, total int, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		if size == 0 {
			size = 20
		}
		start := (page - 1) * size
		items := []string{}
		for i := start; i < start+size && i < total; i++ {
			items = append(items, fmt.Sprintf(
				`{"id":"id%04d","title":"Page %d","url":"https://x/s/id%04d","view_count":%d,"latest_version":2,"shared_version":0,"visibility":"link","create_time":"2026-08-01T12:00:00Z","expires_at":null}`,
				i, i, i, i))
		}
		fmt.Fprintf(w, `{"files":[%s],"total":%d,"page":%d,"page_size":%d}`,
			strings.Join(items, ","), total, page, size)
	}))
}

func TestLsAutoPaginatesWithPageSize100(t *testing.T) {
	var hits int32
	srv := pagedFilesServer(t, 250, &hits)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "ls"); err != nil {
		t.Fatalf("ls: %v", err)
	}
	var got struct {
		Files []map[string]any `json:"files"`
		Total int64            `json:"total"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Files) != 250 || got.Total != 250 {
		t.Fatalf("got %d files (total %d), want 250", len(got.Files), got.Total)
	}
	if hits != 3 {
		t.Fatalf("requests = %d, want 3 (250 / page_size 100)", hits)
	}
}

func TestLsJSONOmitsPageFields(t *testing.T) {
	var hits int32
	srv := pagedFilesServer(t, 5, &hits)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "ls"); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["page"]; ok {
		t.Error("page must not appear after auto-pagination")
	}
	if _, ok := raw["page_size"]; ok {
		t.Error("page_size must not appear after auto-pagination")
	}
}

func TestLsLimitStopsEarly(t *testing.T) {
	var hits int32
	srv := pagedFilesServer(t, 250, &hits)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "ls", "--limit", "5"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Files []map[string]any `json:"files"`
	}
	_ = json.Unmarshal(stdout.Bytes(), &got)
	if len(got.Files) != 5 {
		t.Fatalf("got %d files, want 5", len(got.Files))
	}
	if hits != 1 {
		t.Fatalf("requests = %d, want 1 (stop as soon as we have enough)", hits)
	}
}

func TestLsExplicitPageDisablesAutoPagination(t *testing.T) {
	var hits int32
	srv := pagedFilesServer(t, 250, &hits)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "ls", "--page", "2", "--page-size", "20"); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("requests = %d, want exactly 1 (escape hatch = one raw request)", hits)
	}
	// Escape hatch passes the server response through verbatim, page fields and all.
	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["page"] != float64(2) || raw["page_size"] != float64(20) {
		t.Fatalf("escape hatch must pass the server body through: %v", raw)
	}
}

func TestLsPageCapWarnsAndDoesNotTruncateSilently(t *testing.T) {
	var hits int32
	// 6000 files > 50 pages × 100.
	srv := pagedFilesServer(t, 6000, &hits)
	defer srv.Close()

	env, _, stderr := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "ls"); err != nil {
		t.Fatal(err)
	}
	if hits != 50 {
		t.Fatalf("requests = %d, want the 50-page hard cap", hits)
	}
	if !strings.Contains(stderr.String(), "--page") {
		t.Fatalf("hitting the cap must warn and suggest manual paging, stderr = %q", stderr.String())
	}
}

func TestLsHumanRendersSharedVersionZeroAsLatest(t *testing.T) {
	var hits int32
	srv := pagedFilesServer(t, 2, &hits)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--no-color", "ls"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "\t0\t") {
		t.Fatal("shared_version 0 must never be shown as 0 in human mode")
	}
	assertGolden(t, "ls_human.golden", stdout.Bytes())
}

func TestLsJSONKeepsSharedVersionZero(t *testing.T) {
	var hits int32
	srv := pagedFilesServer(t, 1, &hits)
	defer srv.Close()

	env, stdout, _ := newTestEnv(t, srv.URL)
	env.HTTP = srv.Client()
	if err := runCLI(env, "--base", srv.URL, "--json", "ls"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"shared_version":0`) {
		t.Fatalf("--json must keep the raw 0, never translate to \"latest\": %s", stdout.String())
	}
}
