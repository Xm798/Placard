package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// maxTextResponseSize caps checksums.txt reads (1 MB). The release list has
	// its own cap inside the ghrelease package.
	maxTextResponseSize = 1 << 20
	// maxBinaryResponseSize caps the binary download (128 MB). The CLI is a few
	// tens of MB; this is a backstop, not a target.
	maxBinaryResponseSize = 128 << 20
)

// newTransport bounds connection setup so a dead endpoint fails fast no matter
// how generous the client's overall timeout is.
func newTransport() *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
}

// textClient handles small metadata responses. 60 s is plenty and fails fast.
var textClient = &http.Client{Timeout: 60 * time.Second, Transport: newTransport()}

// binaryClient downloads the release binary. A 60 s cap would break legitimate
// downloads on throttled or VPN links, so the total cap is generous while the
// transport's ResponseHeaderTimeout still kills dead servers in 15 s.
var binaryClient = &http.Client{Timeout: 10 * time.Minute, Transport: newTransport()}

func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := textClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTextResponseSize))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", url, err)
	}
	return strings.TrimSpace(string(body)), nil
}

func fetchBinary(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := binaryClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBinaryResponseSize))
}

// verifySHA256 compares data against a hex-encoded digest. An empty expected
// digest is an error, never a pass — self-update has no skip path.
func verifySHA256(data []byte, expected string) error {
	if expected == "" {
		return fmt.Errorf("no sha256 digest available")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("sha256 mismatch (expected %s, got %s)", expected, got)
	}
	return nil
}
