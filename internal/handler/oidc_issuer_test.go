package handler

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeIssuer is a minimal OpenID Connect provider on an httptest.Server: a
// discovery document, a JWKS, a token endpoint and a userinfo endpoint, with id
// tokens signed by a per-test RSA key.
//
// It is the only new test seam the OIDC work needs. Everything above it — the
// registry, the flow store, provisioning, linking — is driven through the real
// HTTP routes against this, so the tests exercise signature and nonce
// verification rather than a stub that returns claims directly.
type fakeIssuer struct {
	server *httptest.Server
	// key is what the JWKS publishes; signKey is what id tokens are actually
	// signed with. They are the same key until a test points signKey at
	// another one to forge a token the published JWKS cannot verify.
	key     *rsa.PrivateKey
	signKey *rsa.PrivateKey

	// codes maps an authorization code to the claims its id token carries and
	// the nonce it must echo; access holds the same claims under the access
	// token the code redeems to, which is what userinfo answers on. Both are
	// registered by the test before the callback is driven.
	codes  map[string]issuedToken
	access map[string]map[string]interface{}

	// verifiers records the PKCE code_verifier each redemption sent, keyed by
	// the code it redeemed.
	verifiers map[string]string
}

type issuedToken struct {
	nonce  string
	claims map[string]interface{}
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate issuer key: %v", err)
	}
	f := &fakeIssuer{
		key:       key,
		signKey:   key,
		codes:     map[string]issuedToken{},
		access:    map[string]map[string]interface{}{},
		verifiers: map[string]string{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/jwks", f.jwks)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/userinfo", f.userinfo)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, _ *http.Request) {
		// Never reached: the tests read the redirect the server produced
		// instead of following the browser leg to the provider.
		w.WriteHeader(http.StatusNoContent)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// URL is the issuer identifier, which is also the discovery base and the value
// go-oidc compares the id token's "iss" claim against.
func (f *fakeIssuer) URL() string { return f.server.URL }

// issue registers an authorization code and the claims redeeming it returns.
func (f *fakeIssuer) issue(code, nonce string, claims map[string]interface{}) {
	f.codes[code] = issuedToken{nonce: nonce, claims: claims}
	f.access["at-"+code] = claims
}

func (f *fakeIssuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]interface{}{
		"issuer":                                f.server.URL,
		"authorization_endpoint":                f.server.URL + "/authorize",
		"token_endpoint":                        f.server.URL + "/token",
		"jwks_uri":                              f.server.URL + "/jwks",
		"userinfo_endpoint":                     f.server.URL + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIssuer) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.Public().(*rsa.PublicKey)
	writeJSON(w, map[string]interface{}{"keys": []map[string]string{{
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"kid": "test",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.verifiers[r.PostFormValue("code")] = r.PostFormValue("code_verifier")
	issued, ok := f.codes[r.PostFormValue("code")]
	if !ok {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	writeJSON(w, map[string]interface{}{
		"access_token": "at-" + r.PostFormValue("code"),
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     f.signIDToken(clientIDOf(r), issued),
	})
}

// clientIDOf reads the client the token request authenticated as. oauth2
// prefers HTTP Basic and falls back to form fields, so a fake that only looked
// at the form would mint id tokens with an empty audience.
func clientIDOf(r *http.Request) string {
	if id, _, ok := r.BasicAuth(); ok {
		return id
	}
	return r.PostFormValue("client_id")
}

// userinfo answers with the same claims the id token carried, which is what a
// real provider does. The merge in the oidc package only fills gaps, so this
// changes nothing for a complete id token and covers the endpoint being called
// at all.
func (f *fakeIssuer) userinfo(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, ok := f.access[token]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	writeJSON(w, claims)
}

// signIDToken builds an RS256 JWT out of the registered claims plus the
// registered claims that make it verifiable: issuer, audience, nonce and
// lifetime.
//
// clientID comes from the token request rather than from a fixture constant,
// so an "aud" mismatch can only ever be produced deliberately by a test that
// signs one itself.
func (f *fakeIssuer) signIDToken(clientID string, issued issuedToken) string {
	claims := map[string]interface{}{
		"iss":   f.server.URL,
		"aud":   clientID,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"nonce": issued.nonce,
	}
	for k, v := range issued.claims {
		claims[k] = v
	}
	return signJWT(f.signKey, claims)
}

// signJWT renders a compact RS256 JWT. Written out rather than pulled from a
// JWT library: the test's job is to produce exactly what go-oidc verifies,
// including the malformed cases a library would refuse to build.
func signJWT(key *rsa.PrivateKey, claims map[string]interface{}) string {
	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test"})
	payload, _ := json.Marshal(claims)
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)

	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic("sign test id_token: " + err.Error())
	}
	return signing + "." + enc.EncodeToString(sig)
}

func writeJSON(w http.ResponseWriter, body interface{}) {
	writeJSONStatus(w, http.StatusOK, body)
}

func writeJSONStatus(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
