// cmd/placard/client.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Xm798/placard/internal/dto"
)

// maxResponseBody caps what the CLI will buffer from an API response. The API
// surface is metadata-only (page bytes are never returned to a PAT channel), so
// 8 MiB is generous; the cap exists so a misrouted proxy cannot stream forever.
const maxResponseBody = 8 << 20

// defaultHTTPTimeout covers metadata calls. Publish uploads use the same
// client: the server's own body limit bounds the payload.
const defaultHTTPTimeout = 60 * time.Second

type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func NewClient(base, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: hc}
}

// Do performs one request and returns (status, body, error). A non-2xx status
// is converted into a typed *Error via parseAPIError, so callers never inspect
// status codes themselves.
func (c *Client) Do(ctx context.Context, method, path string, q url.Values, body []byte, contentType string) (int, []byte, error) {
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return 0, nil, Usage("invalid request URL: " + err.Error())
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "placard-cli/"+cliVersion())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// No token means an intentionally anonymous call (device-code endpoints).
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, nil, &Error{Code: "network", Message: "request canceled", Exit: 1}
		}
		return 0, nil, Network("request " + u + " failed: " + err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if rerr != nil {
		return resp.StatusCode, nil, Network("failed to read response: " + rerr.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, raw, parseAPIError(resp.StatusCode, raw)
	}
	return resp.StatusCode, raw, nil
}

// DoJSON sends reqBody as JSON (when non-nil), decodes into out (when non-nil)
// and always returns the raw body so a command can pass the server's bytes
// through verbatim in --json mode.
func (c *Client) DoJSON(ctx context.Context, method, path string, q url.Values, reqBody, out any) ([]byte, error) {
	var body []byte
	contentType := ""
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, LocalIO("failed to build request body: " + err.Error())
		}
		body, contentType = b, "application/json"
	}
	_, raw, err := c.Do(ctx, method, path, q, body, contentType)
	if err != nil {
		return nil, err
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, Network("failed to parse server response: " + err.Error())
		}
	}
	return raw, nil
}

// DoMultipart posts a multipart/form-data body: one "file" part plus the
// non-empty text fields. Empty fields are omitted so the server's
// "omitted means default" semantics are preserved.
func (c *Client) DoMultipart(ctx context.Context, path string, fields map[string]string, fileName string, file []byte, out any) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// Sorted iteration is unnecessary for correctness but keeps request bodies
	// byte-stable across runs, which makes failures easier to diff.
	for _, k := range sortedKeys(fields) {
		if fields[k] == "" {
			continue
		}
		if err := mw.WriteField(k, fields[k]); err != nil {
			return nil, LocalIO("failed to build multipart body: " + err.Error())
		}
	}
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		return nil, LocalIO("failed to build multipart body: " + err.Error())
	}
	if _, err := fw.Write(file); err != nil {
		return nil, LocalIO("failed to write multipart body: " + err.Error())
	}
	if err := mw.Close(); err != nil {
		return nil, LocalIO("failed to close multipart body: " + err.Error())
	}
	_, raw, err := c.Do(ctx, http.MethodPost, path, nil, buf.Bytes(), mw.FormDataContentType())
	if err != nil {
		return nil, err
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return raw, Network("failed to parse server response: " + err.Error())
		}
	}
	return raw, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// parseAPIError maps the unified server envelope (dto.ErrorResponse) onto a
// typed CLI error. Two rules are load-bearing:
//
//   - 404 + code "error" means the ROUTE does not exist (Fiber's *fiber.Error
//     branch) => server_too_old. 404 + code "not_found" is a normal business
//     miss. The envelope code is the ONLY discriminator; the message text
//     ("Cannot POST ...") is a Fiber implementation detail and must never be
//     matched or shown (spec §13.1).
//   - 415 + code "validation" is the one case where "validation" is not a 400
//     (publish.go's non-HTML rejection), so status is checked before code.
//   - 426 is the server's min_cli_version gate. It is keyed on the STATUS
//     rather than the envelope code so that a server wording the envelope
//     differently still produces an upgrade instruction rather than a bare
//     "HTTP 426".
func parseAPIError(status int, body []byte) *Error {
	var env dto.ErrorResponse
	_ = json.Unmarshal(body, &env)
	code, msg := env.Code, env.Message
	if code == "" {
		code = "error"
	}
	if msg == "" {
		msg = fmt.Sprintf("server returned HTTP %d", status)
	}

	if status == 404 && code == "error" {
		return ServerTooOld("this Placard server does not have this endpoint and may be too old; contact your administrator to upgrade the server", 1)
	}
	if status == 415 && code == "validation" {
		return ServerError(code, "file is not HTML (the server only accepts HTML content)", 1)
	}
	if status == 426 {
		// The server's own message names the command; only add it when it does
		// not, which is what an unusual envelope or an empty body leaves.
		if !strings.Contains(msg, "placard update") {
			msg += "\nRun: placard update"
		}
		return ServerError("upgrade_required", msg, 1)
	}

	switch code {
	case "unauthorized":
		return ServerError(code, "not authenticated or credentials expired; run: placard login", 3)
	case "permission_denied":
		return ServerError(code, msg, 1)
	case "not_found":
		return ServerError(code, "page does not exist or is not owned by you", 1)
	case "validation":
		return ServerError(code, msg, 1)
	case "rate_limited":
		// The server sends no Retry-After, so never invent a wait time.
		return ServerError(code, "rate limited, please retry later", 1)
	case "storage_failed":
		return ServerError(code, "storage backend error, please retry later", 1)
	case "unavailable":
		return ServerError(code, "service temporarily unavailable, please retry later", 1)
	case "internal":
		return ServerError(code, "internal server error", 1)
	default:
		return ServerError(code, msg, 1)
	}
}

// rateLimitHint replaces the generic 429 wording with an endpoint-specific one.
// Publish/restore share one hourly budget; /api/users/resolve has its own
// per-minute budget — the two must not borrow each other's wording (spec §10.2).
func rateLimitHint(err error, hint string) error {
	var e *Error
	if errors.As(err, &e) && e.Code == "rate_limited" {
		return ServerError(e.Code, hint, e.Exit)
	}
	return err
}

// asServerTooOld rewrites the generic missing-route error with command-specific
// guidance and exit code (login uses 3, everything else 1).
func asServerTooOld(err error, message string, exit int) error {
	var e *Error
	if errors.As(err, &e) && e.Code == "server_too_old" {
		return ServerTooOld(message, exit)
	}
	return err
}
