package storage

import (
	"fmt"

	"github.com/Xm798/placard/internal/config"
)

// NewFromConfig builds the storage Client main.go should wire, selected by
// storage.type alone — an empty or misconfigured backend is an error, never a
// silent downgrade to the in-memory stub (which would accept publishes and
// lose them on restart).
func NewFromConfig(cfg config.StorageConfig) (Client, error) {
	switch cfg.Type {
	case "", "local":
		return NewLocalClient(cfg.Local)
	case "s3":
		return NewS3Client(cfg.S3)
	default:
		return nil, fmt.Errorf("storage: unknown type %q (want local or s3)", cfg.Type)
	}
}
