package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/Xm798/placard/internal/model"
	placardskill "github.com/Xm798/placard/skills/placard"
)

// republish POSTs /api/publish with an id — the update-in-place path. html
// must not contain double quotes (raw JSON splice, same style as publishOne).
func republish(t *testing.T, app *fiber.App, id, html, title string) (int, []byte) {
	t.Helper()
	// Marshalled rather than concatenated: html carries quoted attributes in
	// the tests that exercise the <meta> snapshots.
	body, err := json.Marshal(map[string]string{"id": id, "html": html, "title": title})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return doJSON(t, app, "POST", "/api/publish", string(body))
}

type publishResp struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Version      int       `json:"version"`
	SkillVersion int       `json:"skill_version"`
	CreateTime   time.Time `json:"create_time"`
}

func decodePublish(t *testing.T, b []byte) publishResp {
	t.Helper()
	var pr publishResp
	if err := json.Unmarshal(b, &pr); err != nil {
		t.Fatalf("unmarshal publish resp %s: %v", b, err)
	}
	return pr
}

// assertServingCache asserts the spec's core invariant after every mutation:
// file.object_key/size_bytes/title equal the resolved serving version row
// (shared_version, or latest_version when shared_version = 0).
func assertServingCache(t *testing.T, deps Deps, nanoID string) {
	t.Helper()
	var f model.File
	if err := deps.DB.Where("nano_id = ?", nanoID).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	serving := f.SharedVersion
	if serving == 0 {
		serving = f.LatestVersion
	}
	v, err := deps.Versions.GetByVersion(context.Background(), nanoID, serving)
	if err != nil {
		t.Fatalf("resolve serving version %d: %v", serving, err)
	}
	if f.ObjectKey != v.ObjectKey || f.SizeBytes != v.SizeBytes ||
		f.Title != v.Title || f.Description != v.Description {
		t.Fatalf("serving cache diverged: file={%s %d %q %q}, version %d={%s %d %q %q}",
			f.ObjectKey, f.SizeBytes, f.Title, f.Description,
			serving, v.ObjectKey, v.SizeBytes, v.Title, v.Description)
	}
}

// seedForeignFile creates a live file (+ its v1 row) owned by `owner`,
// bypassing the API — for non-owner 404 checks.
func seedForeignFile(t *testing.T, deps Deps, nanoID, owner string) {
	t.Helper()
	h := strings.Repeat("ef", 32)
	f := &model.File{NanoID: nanoID, Title: "not yours", ObjectKey: "2026/07/" + nanoID + "-1.html",
		SizeBytes: 2, LatestVersion: 1, ExpiresAt: neverSentinel, CreateUser: owner, UpdateUser: owner}
	if err := deps.DB.Create(f).Error; err != nil {
		t.Fatalf("seed foreign file: %v", err)
	}
	v := &model.FileVersion{NanoID: nanoID, Version: 1, ObjectKey: f.ObjectKey, SizeBytes: 2,
		ContentHash: h, Title: f.Title, CreateUser: owner}
	if err := deps.DB.Create(v).Error; err != nil {
		t.Fatalf("seed foreign version: %v", err)
	}
}

// versionRow reads one persisted version row, for assertions that compare a
// response field against what actually landed in the database.
func versionRow(t *testing.T, deps Deps, nanoID string, version int) model.FileVersion {
	t.Helper()
	var v model.FileVersion
	if err := deps.DB.Where("nano_id = ? AND version = ?", nanoID, version).First(&v).Error; err != nil {
		t.Fatalf("read version %d row: %v", version, err)
	}
	return v
}

// assertEchoedCreateTime asserts an echoed create_time is neither the Go zero
// time nor a rounding-mangled copy of the row it came from. The zero-time check
// is the whole point: the zero time serializes to the perfectly non-empty
// "0001-01-01T00:00:00Z", so a mere non-empty assertion passes against the bug
// (that is exactly how it shipped). Exact equality with the persisted value pins
// the repo layer's truncation — create_time is a fractionless datetime, so an
// untruncated timestamp would round on store and diverge from what the caller
// was told, making the echoed value disagree with a later GET.
func assertEchoedCreateTime(t *testing.T, label string, echoed, persisted time.Time) {
	t.Helper()
	if echoed.IsZero() {
		t.Fatalf("%s: create_time = %s (Go zero time) — the column was dropped from the INSERT, so the field was never set",
			label, echoed.Format(time.RFC3339))
	}
	if !echoed.Equal(persisted) {
		t.Fatalf("%s: echoed create_time %s != persisted %s",
			label, echoed.Format(time.RFC3339Nano), persisted.Format(time.RFC3339Nano))
	}
}

// TestPublishPathsEchoNonZeroCreateTime guards every response path that reports
// a create_time. The three INSERT paths (first publish, republish with an id,
// restore) each build their response from an in-memory struct that GORM never
// wrote a timestamp back into, so each one used to echo the Go zero time while
// the DB row was correct. The idempotent hash hit reads its row back from the DB
// and was always right — it is here so it stays that way.
func TestPublishPathsEchoNonZeroCreateTime(t *testing.T) {
	app, deps := newTestApp(t)

	// First publish: the response echoes the new file row's create_time.
	code, b := doJSON(t, app, "POST", "/api/publish",
		`{"html":"<html><head><title>V1</title></head><body>one</body></html>"}`)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d %s", code, b)
	}
	first := decodePublish(t, b)
	var file model.File
	if err := deps.DB.Where("nano_id = ?", first.ID).First(&file).Error; err != nil {
		t.Fatalf("read file row: %v", err)
	}
	assertEchoedCreateTime(t, "first publish", first.CreateTime, file.CreateTime)
	// update_time is double-tagged the same way and so shares the defect; it is
	// not echoed anywhere today, but the INSERT must still carry it.
	if file.UpdateTime.IsZero() {
		t.Fatalf("file.update_time is the zero time after INSERT")
	}

	// Republish with an id: the response echoes the NEW version row's create_time.
	const v2HTML = "<html><head><title>V2</title></head><body>two</body></html>"
	code, b = republish(t, app, first.ID, v2HTML, "")
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	second := decodePublish(t, b)
	if second.Version != 2 {
		t.Fatalf("republish version = %d, want 2", second.Version)
	}
	assertEchoedCreateTime(t, "republish", second.CreateTime, versionRow(t, deps, first.ID, 2).CreateTime)

	// Identical content again: idempotent hash hit, v2 read back from the DB.
	code, b = republish(t, app, first.ID, v2HTML, "")
	if code != fiber.StatusOK {
		t.Fatalf("idempotent republish = %d %s", code, b)
	}
	idem := decodePublish(t, b)
	if idem.Version != 2 {
		t.Fatalf("idempotent version = %d, want 2 (no new row)", idem.Version)
	}
	assertEchoedCreateTime(t, "idempotent hash hit", idem.CreateTime, versionRow(t, deps, first.ID, 2).CreateTime)

	// Restore: publishNewVersion again, reached through the restore endpoint.
	code, b = doJSON(t, app, "POST", "/api/files/"+first.ID+"/versions/1/restore", "")
	if code != fiber.StatusOK {
		t.Fatalf("restore = %d %s", code, b)
	}
	restored := decodePublish(t, b)
	if restored.Version != 3 {
		t.Fatalf("restore version = %d, want 3", restored.Version)
	}
	assertEchoedCreateTime(t, "restore", restored.CreateTime, versionRow(t, deps, first.ID, 3).CreateTime)
}

func TestPublishInsertsVersionOne(t *testing.T) {
	app, deps := newTestApp(t)
	code, b := doJSON(t, app, "POST", "/api/publish",
		`{"html":"<html><head><title>T1</title></head><body>x</body></html>","title":"req"}`)
	if code != fiber.StatusOK {
		t.Fatalf("publish = %d %s", code, b)
	}
	pr := decodePublish(t, b)
	if pr.Version != 1 {
		t.Fatalf("version = %d, want 1", pr.Version)
	}
	if pr.SkillVersion != placardskill.SkillVersion {
		t.Fatalf("skill_version = %d, want %d", pr.SkillVersion, placardskill.SkillVersion)
	}
	if pr.Title != "T1" {
		t.Fatalf("title = %q, want the html <title> snapshot", pr.Title)
	}
	var v model.FileVersion
	if err := deps.DB.Where("nano_id = ? AND version = 1", pr.ID).First(&v).Error; err != nil {
		t.Fatalf("v1 row missing: %v", err)
	}
	if v.ContentHash == "" || len(v.ContentHash) != 64 {
		t.Fatalf("content_hash = %v, want 64-char hex", v.ContentHash)
	}
	if v.Title != "T1" {
		t.Fatalf("version title = %q, want T1", v.Title)
	}
	assertServingCache(t, deps, pr.ID)
}

func TestRepublishAdvancesHead(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never")

	code, b := republish(t, app, id, "<html><head><title>V2 Page</title></head><body>two</body></html>", "")
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	pr := decodePublish(t, b)
	if pr.ID != id {
		t.Fatalf("id changed %s -> %s: the share link must never change", id, pr.ID)
	}
	if pr.Version != 2 || pr.Title != "V2 Page" {
		t.Fatalf("resp = %+v, want version 2 / title V2 Page", pr)
	}

	var f model.File
	if err := deps.DB.Where("nano_id = ?", id).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.LatestVersion != 2 || f.SharedVersion != 0 {
		t.Fatalf("pointers = latest %d shared %d, want 2/0", f.LatestVersion, f.SharedVersion)
	}
	v2KeyPattern := regexp.MustCompile(regexp.QuoteMeta(id) + `-2-[0-9a-f]{8}\.html$`)
	if !v2KeyPattern.MatchString(f.ObjectKey) {
		t.Fatalf("serving object_key = %q, want the v2 key", f.ObjectKey)
	}
	assertServingCache(t, deps, id)

	var audit model.AuditLog
	if err := deps.DB.Where("action = ? AND file_nano_id = ?", "file.republish", id).First(&audit).Error; err != nil {
		t.Fatalf("file.republish audit missing: %v", err)
	}
	if !strings.Contains(audit.Details, `"version":2`) {
		t.Fatalf("audit details = %q, want version 2 payload", audit.Details)
	}
}

func TestRepublishIdenticalContentIsIdempotent(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never") // body: <!DOCTYPE html><html><body>hi</body></html>

	code, b := republish(t, app, id, "<!DOCTYPE html><html><body>hi</body></html>", "Secret Title")
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	if pr := decodePublish(t, b); pr.Version != 1 {
		t.Fatalf("version = %d, want 1 (idempotent hash hit)", pr.Version)
	}
	var n int64
	deps.DB.Model(&model.FileVersion{}).Where("nano_id = ?", id).Count(&n)
	if n != 1 {
		t.Fatalf("version rows = %d, want 1 (no new row on identical content)", n)
	}
}

func TestRepublishIdenticalContentWithEmptyHashCreatesVersion(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never")
	// Simulate a backfilled row (content_hash = '', migration 0002's empty-hash
	// sentinel for pre-versioning files) rather than a real computed hash.
	if err := deps.DB.Model(&model.FileVersion{}).
		Where("nano_id = ? AND version = ?", id, 1).
		Update("content_hash", "").Error; err != nil {
		t.Fatalf("clear content hash: %v", err)
	}

	code, b := republish(t, app, id, "<!DOCTYPE html><html><body>hi</body></html>", "Secret Title")
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	if pr := decodePublish(t, b); pr.Version != 2 {
		t.Fatalf("version = %d, want 2 for empty content_hash", pr.Version)
	}
}

// TestRepublishExpiredIs404 asserts republishing an expired-but-not-yet-
// soft-deleted page 404s — same indistinguishable miss as an owner-scope
// miss, even though GetOwned alone (owner + is_deleted=0) would still find
// the row (final-review hardening: the cleanup cron's soft-delete pass may
// not have run yet).
func TestRepublishExpiredIs404(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")
	if err := deps.DB.Model(&model.File{}).
		Where("nano_id = ?", id).
		Update("expires_at", model.Timestamp(time.Now().Add(-time.Hour))).Error; err != nil {
		t.Fatalf("expire file: %v", err)
	}
	code, _ := republish(t, app, id, "<html><body>x</body></html>", "")
	if code != fiber.StatusNotFound {
		t.Fatalf("republish expired code = %d, want 404", code)
	}
}

func TestRepublishNonOwnerIs404(t *testing.T) {
	app, deps := newTestApp(t)
	seedForeignFile(t, deps, "notmine1", "u_bob")
	code, _ := republish(t, app, "notmine1", "<html><body>x</body></html>", "")
	if code != fiber.StatusNotFound {
		t.Fatalf("code = %d, want 404 (no existence confirmation)", code)
	}
}

func TestRepublishPinnedKeepsServingCache(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never")
	if err := deps.DB.Model(&model.File{}).Where("nano_id = ?", id).
		Update("shared_version", 1).Error; err != nil {
		t.Fatalf("pin v1: %v", err)
	}

	code, b := republish(t, app, id, "<html><head><title>V2</title></head><body>two</body></html>", "")
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	var f model.File
	if err := deps.DB.Where("nano_id = ?", id).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.LatestVersion != 2 {
		t.Fatalf("latest_version = %d, want 2", f.LatestVersion)
	}
	if !strings.HasSuffix(f.ObjectKey, id+"-1.html") {
		t.Fatalf("pinned serving cache moved to %q", f.ObjectKey)
	}
	assertServingCache(t, deps, id)
}

func TestRepublishAfterConcurrentAdvanceReadsFreshHead(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never")
	// Simulate a fully-committed concurrent publish: v2 row + head advanced.
	h2 := strings.Repeat("ab", 32)
	v2 := &model.FileVersion{NanoID: id, Version: 2, ObjectKey: "2026/07/" + id + "-2.html",
		SizeBytes: 3, ContentHash: h2, Title: "t2", CreateUser: testAuthzID}
	if err := deps.DB.Create(v2).Error; err != nil {
		t.Fatalf("seed racer version: %v", err)
	}
	if err := deps.DB.Model(&model.File{}).Where("nano_id = ?", id).
		Updates(map[string]interface{}{"latest_version": 2, "object_key": v2.ObjectKey, "size_bytes": 3, "title": "t2"}).Error; err != nil {
		t.Fatalf("advance racer head: %v", err)
	}

	code, b := republish(t, app, id, "<html><head><title>V3</title></head><body>three</body></html>", "")
	if code != fiber.StatusOK {
		t.Fatalf("republish = %d %s", code, b)
	}
	if pr := decodePublish(t, b); pr.Version != 3 {
		t.Fatalf("version = %d, want 3 (built on the fresh head)", pr.Version)
	}
	assertServingCache(t, deps, id)
}

func TestRepublishExhaustsRetriesOnPoisonedSlot(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "never")
	// Synthetic driver for the retry/orphan branch: a version row occupies the
	// next slot but the head never advanced (cannot happen in production — the
	// insert and the CAS commit in one tx). Every attempt re-reads head=1,
	// re-collides on uk_nano_version, orphans its upload, and after
	// nanoIDMaxRetries the publish fails closed.
	h2 := strings.Repeat("cd", 32)
	v2 := &model.FileVersion{NanoID: id, Version: 2, ObjectKey: "2026/07/" + id + "-2.html",
		SizeBytes: 3, ContentHash: h2, Title: "t2", CreateUser: testAuthzID}
	if err := deps.DB.Create(v2).Error; err != nil {
		t.Fatalf("seed poison version: %v", err)
	}

	code, _ := republish(t, app, id, "<html><body>new</body></html>", "")
	if code != fiber.StatusInternalServerError {
		t.Fatalf("code = %d, want 500 after exhausted CAS retries", code)
	}
	var row model.PendingObjectDelete
	if err := deps.DB.Where("reason = ?", model.ReasonVersionConflict).First(&row).Error; err != nil {
		t.Fatalf("version_conflict orphan not enqueued: %v", err)
	}
}

// Concurrent republishes of one page must produce a contiguous version
// sequence with no duplicates: the head advance is a CAS (repo.AdvanceHead),
// and a publisher that loses the race re-reads the head and retries rather than
// overwriting the winner's version.
//
// On SQLite the single-writer pool serializes the transactions, so this asserts
// the retry loop is correct; run with -tags=integration to put it on Postgres,
// where the writers genuinely overlap.
func TestConcurrentRepublishVersionCAS(t *testing.T) {
	app, deps := newTestApp(t)
	id := publishOne(t, app, "30d")

	// One publisher can lose at most publishers-1 races, and the loop gives up
	// after nanoIDMaxRetries attempts (a deliberate fail-closed bound) — so the
	// concurrency here is what that budget allows, not an arbitrary number.
	const publishers = nanoIDMaxRetries
	errs := make(chan error, publishers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			html := "<!DOCTYPE html><html><body>v" + strconv.Itoa(i) + "</body></html>"
			code, body := republish(t, app, id, html, "T"+strconv.Itoa(i))
			if code != fiber.StatusOK {
				errs <- fmt.Errorf("republish %d: status %d, body %s", i, code, body)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var versions []int
	if err := deps.DB.Model(&model.FileVersion{}).
		Where("nano_id = ?", id).Order("version").Pluck("version", &versions).Error; err != nil {
		t.Fatalf("list versions: %v", err)
	}
	want := make([]int, 0, publishers+1)
	for v := 1; v <= publishers+1; v++ {
		want = append(want, v)
	}
	if !reflect.DeepEqual(versions, want) {
		t.Fatalf("versions = %v, want %v (a lost CAS dropped or duplicated a version)", versions, want)
	}

	var f model.File
	if err := deps.DB.Where("nano_id = ?", id).First(&f).Error; err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.LatestVersion != publishers+1 {
		t.Fatalf("latest_version = %d, want %d", f.LatestVersion, publishers+1)
	}
	assertServingCache(t, deps, id)
}
