package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Xm798/placard/internal/config"
)

const testRedirectURL = "https://placard.test/auth/oidc/callback"

// stubIssuer is a discovery document plus a token endpoint that records the
// form it was posted. Enough to observe both halves of PKCE: the authorization
// URL the browser is sent to, and the token request the server makes.
//
// id_token verification is deliberately out of scope here — it needs a JWKS
// and a signed token, and the handler package already drives the whole flow
// against a full fake issuer.
type stubIssuer struct {
	server *httptest.Server
	form   url.Values
}

func newStubIssuer(t *testing.T) *stubIssuer {
	t.Helper()
	s := &stubIssuer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":                                s.server.URL,
			"authorization_endpoint":                s.server.URL + "/authorize",
			"token_endpoint":                        s.server.URL + "/token",
			"jwks_uri":                              s.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		s.form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "at",
			"token_type":   "Bearer",
		})
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubIssuer) provider(t *testing.T) *Provider {
	t.Helper()
	r := NewRegistry([]config.OIDCProviderConfig{{
		Name:         "corp",
		Issuer:       s.server.URL,
		ClientID:     "placard-test",
		ClientSecret: "s3cret",
	}}, testRedirectURL, s.server.Client())
	p, err := r.Get("corp")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	return p
}

// s256 is what a provider computes from the verifier it is handed back.
func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// The authorization request carries the challenge and never the verifier: a
// challenge that did not hash from the stored verifier would let a stolen code
// be redeemed by whoever captured it.
func TestAuthCodeURLSendsS256Challenge(t *testing.T) {
	p := newStubIssuer(t).provider(t)

	authURL, verifier, err := p.AuthCodeURL(context.Background(), "st", "nc")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	q := u.Query()

	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	challenge := q.Get("code_challenge")
	if len(challenge) != 43 {
		t.Errorf("code_challenge = %q (%d chars), want 43 base64url chars", challenge, len(challenge))
	}
	if challenge != s256(verifier) {
		t.Errorf("code_challenge = %q, want base64url(SHA256(verifier)) = %q", challenge, s256(verifier))
	}
	if strings.Contains(authURL, verifier) {
		t.Errorf("authorize url leaks the verifier: %q", authURL)
	}
	if got := q.Get("state"); got != "st" {
		t.Errorf("state = %q, want st", got)
	}
	if got := q.Get("nonce"); got != "nc" {
		t.Errorf("nonce = %q, want nc", got)
	}
}

// The redemption proves possession of the flow record by sending back the
// verifier whose challenge started the request.
func TestExchangeSendsCodeVerifier(t *testing.T) {
	issuer := newStubIssuer(t)
	p := issuer.provider(t)
	ctx := context.Background()

	_, verifier, err := p.AuthCodeURL(ctx, "st", "nc")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	// The stub answers without an id_token, so the exchange itself fails; the
	// request it made on the way is what this asserts.
	if _, err := p.Exchange(ctx, "the-code", "nc", verifier); err == nil {
		t.Fatal("Exchange accepted a token response with no id_token")
	}
	if issuer.form == nil {
		t.Fatal("token endpoint was never called")
	}
	if got := issuer.form.Get("code_verifier"); got != verifier {
		t.Errorf("code_verifier = %q, want %q", got, verifier)
	}
	if got := issuer.form.Get("code"); got != "the-code" {
		t.Errorf("code = %q, want the-code", got)
	}
}
