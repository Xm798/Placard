// Package web embeds the Placard frontend — the built React SPA (the publish /
// my files / settings app) and its hashed assets. Embedding keeps the app a
// single self-contained binary (distroless), consistent with the view shell.
package web

import "embed"

// DistFS holds the built React SPA (index.html + hashed /assets/*), embedded
// so the single binary self-serves the frontend. Zero external subresources.
//
//go:embed all:dist
var DistFS embed.FS

// LoggedOutTemplate is the "logged out" landing page served after POST
// /auth/logout. It carries no JavaScript, so nothing on it can start a fresh
// login on its own. The handler fills its copy per Accept-Language.
//
//go:embed logged_out.html
var LoggedOutTemplate string

// OIDCErrorTemplate is the page a failed single sign-on leg renders. Those two
// routes are browser navigations, so a failure has to land on something a
// person can read and get out of — the JSON error envelope every API endpoint
// returns would be a white page with no way back.
//
//go:embed oidc_error.html
var OIDCErrorTemplate string
