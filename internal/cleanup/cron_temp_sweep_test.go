package cleanup

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/Xm798/placard/internal/lock"
	"github.com/Xm798/placard/internal/storage"
	"github.com/Xm798/placard/internal/testutil"
)

// sweepingStorage is a backend that also implements storage.TempSweeper.
type sweepingStorage struct {
	storage.Client
	calls   int
	cutoffs []time.Time
	err     error
}

func (s *sweepingStorage) SweepTemp(_ context.Context, olderThan time.Time) (int, error) {
	s.calls++
	s.cutoffs = append(s.cutoffs, olderThan)
	if s.err != nil {
		return 0, s.err
	}
	return 1, nil
}

// The sweep is a slow-cadence step: running it every round would walk the whole
// object tree hourly to reclaim files that are, by construction, rare.
func TestStep5SweepsOnItsOwnCadence(t *testing.T) {
	db := testutil.OpenTestDB(t)
	objectStore := &sweepingStorage{}
	d := newDeps(db, objectStore)
	d.Locker = lock.NewMemoryLocker()
	d.Cfg.ViewRecomputeEvery = 0 // step3 off, so only step5's cadence is under test

	for round := 1; round <= tempSweepEvery; round++ {
		Run(context.Background(), d, round, zap.NewNop())
	}
	if objectStore.calls != 1 {
		t.Fatalf("SweepTemp called %d times over %d rounds, want 1", objectStore.calls, tempSweepEvery)
	}

	// The cutoff must be in the past by at least the safety age, or the sweep
	// would race a PutObject that is still streaming its body.
	cutoff := objectStore.cutoffs[0]
	if age := time.Since(cutoff); age < tempSweepAge {
		t.Errorf("cutoff was %v old, want at least %v behind now", age, tempSweepAge)
	}
}

// A backend with no staging files to strand (s3, the stub) must not be asked.
func TestStep5SkipsBackendsThatDoNotStage(t *testing.T) {
	db := testutil.OpenTestDB(t)
	d := newDeps(db, storage.NewStubClient())
	d.Locker = lock.NewMemoryLocker()
	d.Cfg.ViewRecomputeEvery = 0

	// Reaching the step at all is the point; a non-sweeper must not panic it.
	for round := 1; round <= tempSweepEvery; round++ {
		Run(context.Background(), d, round, zap.NewNop())
	}
}

// Disk hygiene is never a correctness gate, so a failing sweep must not stop
// the round or bubble out.
func TestStep5SurvivesASweepError(t *testing.T) {
	db := testutil.OpenTestDB(t)
	objectStore := &sweepingStorage{err: errors.New("permission denied")}
	d := newDeps(db, objectStore)
	d.Locker = lock.NewMemoryLocker()
	d.Cfg.ViewRecomputeEvery = 0

	for round := 1; round <= tempSweepEvery; round++ {
		Run(context.Background(), d, round, zap.NewNop())
	}
	if objectStore.calls != 1 {
		t.Errorf("SweepTemp called %d times, want 1", objectStore.calls)
	}
}
