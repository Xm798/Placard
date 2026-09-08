package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// stubClient is an in-memory Client for tests. It keeps object bodies in a map
// so the same-origin render proxy can stream them back, letting handler and
// integration flows run without touching the filesystem or a live S3 service.
type stubClient struct {
	mu      sync.Mutex
	objects map[string][]byte // key -> stored body
}

// NewStubClient builds the in-memory stub Client.
func NewStubClient() Client {
	return &stubClient{objects: make(map[string][]byte)}
}

func (s *stubClient) PutObject(_ context.Context, key string, data io.Reader, _ string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	var body []byte
	if data != nil {
		b, err := io.ReadAll(data)
		if err != nil {
			return fmt.Errorf("storage stub put object %q: %w", key, err)
		}
		body = b
	}
	s.mu.Lock()
	s.objects[key] = body
	s.mu.Unlock()
	return nil
}

// GetObject returns a reader over the stored body. A missing key returns an
// error satisfying errors.Is(err, ErrNotFound), matching the local and s3 backends.
func (s *stubClient) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	s.mu.Lock()
	body, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("storage stub get object %q: %w", key, ErrNotFound)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// DeleteObject removes key from the in-memory store. Idempotent: deleting an
// absent key is a no-op, matching the 404-as-success contract of the real
// backends.
func (s *stubClient) DeleteObject(_ context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}
