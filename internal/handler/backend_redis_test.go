//go:build integration

package handler

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Xm798/placard/internal/devicecode"
	"github.com/Xm798/placard/internal/session"
)

// ephemeral is the login state the handler contract runs on — see the doc on
// the default build's copy in backend_test.go. This build wires the Redis
// stores, so the same contract cases run against a real Redis dialect
// (miniredis) rather than against SQL.
type ephemeral struct {
	sessions    session.Store
	deviceCodes devicecode.Store
	advance     func(time.Duration)
}

func newEphemeral(t *testing.T, _ *gorm.DB, idleTTL, absoluteTTL time.Duration) ephemeral {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return ephemeral{
		sessions:    session.NewRedisStore(rdb, idleTTL, absoluteTTL),
		deviceCodes: devicecode.NewRedisStore(rdb),
		advance:     mr.FastForward,
	}
}
