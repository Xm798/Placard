// Package cleanup runs the in-process expiry/reclamation cron. It is pulled up
// by main.go as a background goroutine (no separate binary, no -mode
// subcommand) and serialized by lock.Locker so exactly one instance acts per
// round — across replicas when Redis backs the lock, within the process when it
// does not.
//
// Four steps per round:
//  1. Expire files: in ONE tx enqueue EVERY version's object key
//     (reason=expired) + soft-delete the row + delete its view rows. No storage
//     call happens in step1 — actual object deletion is step2's job. Because
//     enqueue and soft-delete commit atomically, there is no crash window in
//     which a row leaves ListExpired while its objects are untracked (the old
//     R5 "delete the object first" ordering is obsolete).
//  2. Reconcile the pending_object_delete queue (batch delete / retry / R9 alert).
//  3. Recompute the denormalized view_count cache (low frequency, every N rounds).
//  4. Purge expired ephemeral rows (sessions, device flows) for a deployment
//     that keeps them in SQL rather than in Redis, which expires them itself.
//  5. Sweep staging files a killed process stranded in the local object
//     directory (low frequency; backends that stage nothing are skipped).
package cleanup

import (
	"context"
	"strconv"
	"time"

	"github.com/Xm798/placard/internal/ctxlog"
	"github.com/Xm798/placard/internal/lock"
	"github.com/Xm798/placard/internal/logger"
	"github.com/Xm798/placard/internal/model"
	"github.com/Xm798/placard/internal/repo"
	"github.com/Xm798/placard/internal/storage"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

const (
	lockKey    = "placard:cleanup:lock"
	cronActor  = "system:cron" // audit actor (never a user id) so ops can identify cron writes
	batchLimit = 200

	// tempSweepEvery paces step5 in rounds. Stranded staging files need a
	// killed process to appear at all, so hourly would spend a full walk of the
	// object tree on nothing almost every time.
	tempSweepEvery = 24
	// tempSweepAge is how far behind now the sweep's cutoff sits. It has to
	// exceed the slowest PutObject an instance can have in flight, because a
	// staging file younger than this may still be being written: an upload
	// streaming in over a slow link is indistinguishable from a stranded one
	// except by age.
	tempSweepAge = 24 * time.Hour
)

// Config is the cron's runtime tuning, derived from config.CleanupConfig.
type Config struct {
	Interval            time.Duration
	LockTTL             time.Duration
	RetryMax            int
	ViewRecomputeEvery  int
	UserDeleteRetention time.Duration
}

// Purger reclaims rows whose expiry has passed. The SQL-backed session and
// device-code stores implement it; the Redis-backed ones do not need to, since
// Redis drops an expired key on its own.
type Purger interface {
	PurgeExpired(ctx context.Context, now time.Time) (int64, error)
}

// Deps wires the cron to the repos, the object store and the lock. All fields are
// the same instances main.go already assembled — nothing is constructed here.
type Deps struct {
	DB       *gorm.DB
	Storage  storage.Client
	Locker   lock.Locker
	Files    *repo.FileRepo
	Views    *repo.ViewRepo
	Pending  *repo.PendingObjectDeleteRepo
	Versions *repo.FileVersionRepo
	Audit    *repo.AuditRepo
	// Purgers is step4's work list, empty when Redis holds the ephemeral state.
	Purgers []Purger
	Cfg     Config
}

// Scheduler blocks until ctx is cancelled, running one Run per tick. main.go
// launches it with `go Scheduler(ctx, deps)`.
func Scheduler(ctx context.Context, d Deps) {
	log := logger.Module("cleanup")
	t := time.NewTicker(d.Cfg.Interval)
	defer t.Stop()
	round := 0
	for {
		select {
		case <-ctx.Done():
			log.Info("cleanup scheduler stopped")
			return
		case <-t.C:
			round++
			Run(ctx, d, round, log)
		}
	}
}

// roundContext gives the round its correlation key, the way the RequestID
// middleware gives an HTTP request one: from here on every SQL statement and
// storage call the round makes carries the same request_id into the logs.
//
// The id is per-round rather than a fixed "cron" string so two rounds that
// overlap — a slow one still draining the pending queue while the next tick
// fires — can be told apart in ELK. The cardinality cost is one id per
// interval, which is nothing.
//
// Minted in Run rather than in Scheduler so a round driven directly (tests,
// and any future manual trigger) is correlated the same way a ticked one is.
func roundContext(ctx context.Context, round int) context.Context {
	id := "cron_" + strconv.Itoa(round)
	return ctxlog.WithEntrypoint(ctxlog.WithRequestID(ctx, id), ctxlog.EntrypointCron)
}

// Run is one round: acquire lock → step1 → step2 → (periodic) step3 → step4 →
// (periodic) step5 → release.
// Exposed so tests can drive a single round deterministically.
func Run(ctx context.Context, d Deps, round int, log *zap.Logger) {
	ctx = roundContext(ctx, round)

	release, err := d.Locker.Acquire(ctx, lockKey, d.Cfg.LockTTL)
	if err != nil { // ErrNotAcquired: someone else holds it — skip quietly
		return
	}
	// Release on a fresh ctx: the server's ctx may already be cancelled during
	// shutdown, and the lock's TTL is the backstop if release can't run (R8).
	defer release(context.Background())

	step1ExpireFiles(ctx, d, log)
	step2ReconcilePending(ctx, d, log)
	// ViewRecomputeEvery <= 0 disables step3 (guards against mod-by-zero panic).
	if d.Cfg.ViewRecomputeEvery > 0 && round%d.Cfg.ViewRecomputeEvery == 0 {
		step3RecomputeViewCounts(ctx, d, log)
	}
	step4PurgeExpired(ctx, d, log)
	if round%tempSweepEvery == 0 {
		step5SweepStagedObjects(ctx, d, log)
	}
}

// step1ExpireFiles reclaims expired files by QUEUEING, not deleting: all
// version keys are enqueued (reason=expired, no retention) atomically with the
// soft-delete + view purge, and step2's batch/retry/R9 machinery does the storage
// deletes. A tx failure leaves the row expired-but-live; the next round simply
// re-selects it (InsertBatch is idempotent per key).
func step1ExpireFiles(ctx context.Context, d Deps, log *zap.Logger) {
	files, err := d.Files.ListExpired(ctx, time.Now(), batchLimit)
	if err != nil {
		log.Error("step1 list expired", zap.Error(err))
		return
	}
	for _, f := range files {
		if ctx.Err() != nil {
			return
		}
		keys, err := d.Versions.ReclaimKeys(ctx, f.NanoID, f.ObjectKey)
		if err != nil {
			log.Error("step1 list versions", zap.String("id", f.NanoID), zap.Error(err))
			continue
		}
		err = d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := d.Pending.InsertBatch(tx, keys, model.ReasonExpired, cronActor); e != nil {
				return e
			}
			if e := d.Files.MarkDeleted(tx, f.NanoID, cronActor); e != nil {
				return e
			}
			return d.Views.DeleteByFile(tx, f.NanoID)
		})
		if err != nil {
			log.Error("step1 expire", zap.String("id", f.NanoID), zap.Error(err))
			continue
		}
		_ = d.Audit.Insert(ctx, &model.AuditLog{
			Action: "file.delete", Actor: cronActor, ActorName: "cron",
			FileNanoID: f.NanoID, CreateUser: cronActor,
		})
	}
}

// step2ReconcilePending drains the deferred-delete queue: delete the object,
// drop the row on success, bump retry on failure. A row reaching RetryMax stops
// being returned by ListPending and needs human triage (R9) — hence log.Warn.
func step2ReconcilePending(ctx context.Context, d Deps, log *zap.Logger) {
	userDeleteBefore := time.Now().Add(-d.Cfg.UserDeleteRetention)
	rows, err := d.Pending.ListPending(ctx, d.Cfg.RetryMax, userDeleteBefore, batchLimit)
	if err != nil {
		log.Error("step2 list pending", zap.Error(err))
		return
	}
	for _, p := range rows {
		if ctx.Err() != nil {
			return
		}
		if err := d.Storage.DeleteObject(ctx, p.ObjectKey); err != nil {
			_ = d.Pending.IncrRetry(ctx, p.ID) // at RetryMax ListPending drops it → alerted below (R9)
			log.Warn("step2 retry", zap.String("key", p.ObjectKey), zap.Uint("id", p.ID), zap.Error(err))
			continue
		}
		_ = d.Pending.Delete(ctx, p.ID)
	}

	// R9: rows that exhausted their retries vanish from ListPending, so without
	// this they'd fail silently. Surface the backlog as an error for triage.
	if stuck, err := d.Pending.CountAtRetryMax(ctx, d.Cfg.RetryMax); err != nil {
		log.Error("step2 count stuck", zap.Error(err))
	} else if stuck > 0 {
		log.Error("step2 pending deletes exhausted retries — manual triage required (R9)",
			zap.Int64("stuck_rows", stuck), zap.Int("retry_max", d.Cfg.RetryMax))
	}
}

// step3RecomputeViewCounts rebuilds the denormalized view_count cache (low freq).
//
// It takes the round's ctx rather than minting a Background one: this is the
// heaviest statement the cron issues, so it is the one most likely to show up
// as a slow query — and it used to be the one query in the round that reached
// GORM with no request_id to correlate it back to.
func step3RecomputeViewCounts(ctx context.Context, d Deps, log *zap.Logger) {
	n, err := d.Files.RecomputeViewCounts(ctx)
	if err != nil {
		log.Error("step3 recompute", zap.Error(err))
		return
	}
	log.Info("step3 view_count recomputed", zap.Int64("rows", n))
}

// step4PurgeExpired reclaims the ephemeral rows a SQL-backed deployment
// accumulates: login sessions and device flows past their expiry.
//
// It is space reclamation only, never a correctness gate — every read of those
// tables filters on the expiry column itself, so a round that fails here (or a
// deployment that never runs the cron) serves no stale session.
func step4PurgeExpired(ctx context.Context, d Deps, log *zap.Logger) {
	now := time.Now()
	for _, p := range d.Purgers {
		if ctx.Err() != nil {
			return
		}
		n, err := p.PurgeExpired(ctx, now)
		if err != nil {
			log.Error("step4 purge expired", zap.Error(err))
			continue
		}
		if n > 0 {
			log.Info("step4 expired rows purged", zap.Int64("rows", n))
		}
	}
}

// step5SweepStagedObjects reclaims the staging files the local backend leaves
// behind when the process is killed mid-write. They are referenced by nothing —
// no file row, no pending_object_delete entry — so no other step can see them,
// and without this they accumulate for the life of the instance.
//
// Backends that do not stage writes on our disk (s3, the test stub) do not
// implement storage.TempSweeper and are skipped.
//
// Like step4 this is space reclamation only: a stranded file is unreachable
// through the API (validateKey cannot produce a key naming one), so a failed
// sweep costs disk, never correctness.
func step5SweepStagedObjects(ctx context.Context, d Deps, log *zap.Logger) {
	sweeper, ok := d.Storage.(storage.TempSweeper)
	if !ok {
		return
	}
	n, err := sweeper.SweepTemp(ctx, time.Now().Add(-tempSweepAge))
	// n and err are independent: a sweep that reclaimed most of what it found
	// still reports the entries it could not.
	if n > 0 {
		log.Info("step5 staged objects swept", zap.Int("files", n))
	}
	if err != nil {
		log.Error("step5 sweep staged objects", zap.Error(err))
	}
}
