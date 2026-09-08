// Package oidc wraps go-oidc/oauth2 into the two calls the login flow needs:
// build an authorization URL, and turn the code that comes back into verified
// claims.
//
// Discovery is lazy and cached per provider rather than done at boot: an
// identity provider that is down or slow when the server starts must not stop
// the server from starting, and password logins must keep working while it is
// unreachable. A failed discovery is not cached, so the next login attempt
// retries it.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Xm798/placard/internal/config"
)

// ErrUnknownProvider is returned for a provider name that is not configured.
var ErrUnknownProvider = errors.New("oidc: unknown provider")

// defaultScopes is what a provider gets when it configures none: the id token
// itself, plus the profile and email claims the account is built from.
var defaultScopes = []string{coreoidc.ScopeOpenID, "profile", "email"}

// Claims is the subset of the OpenID Connect standard claims Placard reads.
// Subject is the only one that identifies the account; everything else is
// profile decoration that a provider may omit, change, or lie about, and is
// never used to decide who the caller is.
//
// EmailVerified is what gates linking an upstream identity to an existing
// account by address — an unverified address is an unproven claim to someone
// else's account.
type Claims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Picture           string `json:"picture"`
}

// Descriptor is what the login page needs to render one provider button:
// the key the start endpoint takes and the label to print on it.
type Descriptor struct {
	Name  string `json:"name"`
	Label string `json:"display_name"`
}

// Provider is one configured identity provider plus its cached discovery
// result.
type Provider struct {
	cfg         config.OIDCProviderConfig
	redirectURL string
	client      *http.Client

	mu       sync.Mutex
	resolved *coreoidc.Provider
}

// Name is the stable key stored in user_identity.provider.
func (p *Provider) Name() string { return p.cfg.Name }

// Label is the login button's text.
func (p *Provider) Label() string { return p.cfg.Label() }

// Registry holds the configured providers in the order the config file lists
// them, which is the order the login page renders their buttons in.
type Registry struct {
	ordered []*Provider
	byName  map[string]*Provider
}

// NewRegistry builds the registry from config. redirectURL is the one absolute
// callback URL every provider is configured with upstream; client, when
// non-nil, is used for discovery, the token exchange and the userinfo call
// (tests point it at a fake issuer).
//
// Names are normalized the way config.validateOIDC compares them, so a lookup
// cannot miss a provider that validation already accepted as unique.
func NewRegistry(cfgs []config.OIDCProviderConfig, redirectURL string, client *http.Client) *Registry {
	r := &Registry{byName: make(map[string]*Provider, len(cfgs))}
	for _, c := range cfgs {
		c.Name = strings.ToLower(strings.TrimSpace(c.Name))
		p := &Provider{cfg: c, redirectURL: redirectURL, client: client}
		r.ordered = append(r.ordered, p)
		r.byName[c.Name] = p
	}
	return r
}

// Len is how many providers are configured. Zero means the instance is
// password-only and the OIDC routes have nothing to serve.
func (r *Registry) Len() int { return len(r.ordered) }

// List describes every provider for the login page, in configuration order.
func (r *Registry) List() []Descriptor {
	out := make([]Descriptor, 0, len(r.ordered))
	for _, p := range r.ordered {
		out = append(out, Descriptor{Name: p.cfg.Name, Label: p.Label()})
	}
	return out
}

// Get resolves a provider by name, or ErrUnknownProvider.
func (r *Registry) Get(name string) (*Provider, error) {
	p, ok := r.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, ErrUnknownProvider
	}
	return p, nil
}

// AuthCodeURL is where the browser is sent to authenticate. state and nonce
// are the caller's, stored server-side for the callback to compare against.
func (p *Provider) AuthCodeURL(ctx context.Context, state, nonce string) (string, error) {
	oauthCfg, err := p.oauthConfig(ctx)
	if err != nil {
		return "", err
	}
	return oauthCfg.AuthCodeURL(state, coreoidc.Nonce(nonce)), nil
}

// Exchange redeems the authorization code and returns the verified claims.
//
// Everything a caller is allowed to trust is established here: the token
// response must carry an id_token, its signature and issuer and audience must
// verify against the provider's JWKS, and its nonce must be the one this flow
// started with. A failure at any of those returns an error and no claims —
// there is no partial result the caller could fall back on.
func (p *Provider) Exchange(ctx context.Context, code, nonce string) (*Claims, error) {
	ctx = p.clientContext(ctx)
	oauthCfg, err := p.oauthConfig(ctx)
	if err != nil {
		return nil, err
	}

	tok, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oidc: exchange code: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, errors.New("oidc: token response carried no id_token")
	}

	provider, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	idToken, err := provider.Verifier(&coreoidc.Config{ClientID: p.cfg.ClientID}).Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}
	if idToken.Nonce != nonce {
		return nil, errors.New("oidc: id_token nonce does not match the flow")
	}

	var claims Claims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: decode id_token claims: %w", err)
	}
	if claims.Subject == "" {
		return nil, errors.New("oidc: id_token carried no subject")
	}
	p.mergeUserInfo(ctx, provider, tok, &claims)
	return &claims, nil
}

// mergeUserInfo fills in profile claims the id_token left out from the
// userinfo endpoint. Best-effort on purpose: the identity is already
// established by the verified id_token, so a provider without a working
// userinfo endpoint still logs the user in, just with less decoration.
//
// Only empty fields are filled. A userinfo response for a different subject is
// discarded whole rather than merged — go-oidc checks that too, but a mismatch
// means the response describes someone else and none of it belongs on this
// account.
func (p *Provider) mergeUserInfo(ctx context.Context, provider *coreoidc.Provider, tok *oauth2.Token, claims *Claims) {
	info, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		return
	}
	var extra Claims
	if err := info.Claims(&extra); err != nil || extra.Subject != claims.Subject {
		return
	}
	if claims.Email == "" && extra.Email != "" {
		claims.Email, claims.EmailVerified = extra.Email, extra.EmailVerified
	}
	if claims.PreferredUsername == "" {
		claims.PreferredUsername = extra.PreferredUsername
	}
	if claims.Name == "" {
		claims.Name = extra.Name
	}
	if claims.Picture == "" {
		claims.Picture = extra.Picture
	}
}

// oauthConfig builds the oauth2 config from the discovered endpoints.
func (p *Provider) oauthConfig(ctx context.Context) (*oauth2.Config, error) {
	provider, err := p.discover(p.clientContext(ctx))
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  p.redirectURL,
		Scopes:       p.scopes(),
	}, nil
}

// scopes is the configured scope list with "openid" guaranteed present —
// without it the provider runs a plain OAuth2 flow and returns no id_token,
// which is the only thing this package trusts.
func (p *Provider) scopes() []string {
	if len(p.cfg.Scopes) == 0 {
		return defaultScopes
	}
	for _, s := range p.cfg.Scopes {
		if s == coreoidc.ScopeOpenID {
			return p.cfg.Scopes
		}
	}
	return append([]string{coreoidc.ScopeOpenID}, p.cfg.Scopes...)
}

// discover runs (or replays) OpenID Connect discovery against the issuer.
//
// The result is cached for the process lifetime; a failure is not, so an
// issuer that was down during one login attempt is retried on the next rather
// than being written off until a restart.
func (p *Provider) discover(ctx context.Context) (*coreoidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolved != nil {
		return p.resolved, nil
	}
	provider, err := coreoidc.NewProvider(ctx, p.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover issuer %s: %w", p.cfg.Issuer, err)
	}
	p.resolved = provider
	return provider, nil
}

// clientContext attaches the injected HTTP client, which go-oidc and oauth2
// both read out of the context rather than from a parameter.
func (p *Provider) clientContext(ctx context.Context) context.Context {
	if p.client == nil {
		return ctx
	}
	return coreoidc.ClientContext(ctx, p.client)
}
