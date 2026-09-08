# Placard

**English** · [简体中文](README.zh-CN.md)

Self-hosted publishing for self-contained HTML pages. Upload a single `.html`
file — from the web UI or the `placard` CLI — and get back a share link that
renders it. Built for the reports, dashboards, slide decks and one-off pages
that tools and agents generate but nobody wants to email around as attachments.

- **One binary, zero dependencies.** SQLite and a local object directory out of
  the box. Postgres, S3-compatible storage and Redis are all optional.
- **Real accounts.** Local passwords (argon2id) and any number of OpenID Connect
  providers. The first person to register becomes the administrator.
- **Versioned pages.** Republish in place: the link never changes, and you can
  list, pin and restore earlier versions.
- **Share links you control.** Private or link-visible, an optional expiry, an
  optional 6-digit share code, Open Graph previews, and view counts.
- **Storage stays private.** Page bytes are always proxied through the server,
  so no bucket is ever public and everything is served same-origin.
- **A CLI and an agent skill.** `placard publish report.html` from a terminal,
  CI job or coding agent.

## Quick start

### Docker

```bash
curl -fsSLO https://raw.githubusercontent.com/Xm798/Placard/HEAD/docker-compose.yaml
docker compose up -d
```

Open <http://localhost:8080> and register — the first account created on a fresh
instance is the administrator, and registration closes behind it.

Everything the instance writes lives in the `placard_data` volume. Behind a
domain or a reverse proxy, set `PLACARD_SERVER_BASE_URL` to the URL browsers
actually use: share links are built from it.

### Binary

Download the archive for your platform from
[Releases](https://github.com/Xm798/Placard/releases) (server releases are the
plain `vX.Y.Z` tags), then:

```bash
tar xzf placard-server-*.tar.gz
./placard-server
```

It creates `./data/` (SQLite database, objects, generated instance secret) and
listens on `:8080`. No config file is required.

### From source

Requires Go 1.26+ and Node 22+.

```bash
git clone https://github.com/Xm798/Placard.git
cd Placard
make frontend-install frontend   # the bundle the server embeds
make run                         # SQLite under ./data, listening on :8080
```

## Publishing from the CLI

Install the CLI (a single binary, no sudo, sha256-verified):

```bash
# macOS / Linux
curl -fsSL https://your-placard.example.com/install.sh | sh

# Windows (PowerShell)
irm https://your-placard.example.com/install.ps1 | iex
```

Every instance serves the installer, which fetches the binary from this
project's GitHub Releases (the `cli/v*` tags). If your instance is not
reachable, install straight from the repository instead:

```bash
curl -fsSL https://raw.githubusercontent.com/Xm798/Placard/HEAD/installer/install.sh | sh
```

Then sign in and publish:

```bash
placard login --base https://your-placard.example.com
placard publish report.html
```

Placard has no built-in default server, so the first login must name `--base`;
it is remembered afterwards. `login` is a device-code flow: the terminal prints
an 8-character code, opens a browser, and you confirm the code matches.

```
placard publish <file.html>   publish, or --id <id> to add a version in place
placard ls                    list your pages
placard rm <id>               delete a page
placard open <id>             open the share link in a browser
placard version ls|pin|restore  version history
placard whoami / login / logout / update
```

For CI, create a personal access token on the Settings page and pass it through
the `PLACARD_TOKEN` environment variable rather than the `--token` flag, which
leaks into shell history and `ps`.

### Coding agents

An instance also serves an agent skill at `/skill.md` and its installation
instructions at `/install.md`. Point an agent at
`https://your-placard.example.com/install.md` and it will install the CLI, sign
you in and set itself up to publish.

## Configuration

Configuration comes from defaults, then an optional YAML file, then the
environment. Any scalar key can be set as `PLACARD_` plus the key upper-cased
with dots replaced by underscores — `server.base_url` is
`PLACARD_SERVER_BASE_URL`.

The file is found via `--config`, then `$PLACARD_CONFIG`, then `config.yaml` in
the working directory. [`config.example.yaml`](config.example.yaml) is a
commented starting point, and **[docs/configuration.md](docs/configuration.md)
documents every key, its default and its environment variable**.

### Single sign-on

Any provider with an OpenID Connect discovery document works; there are no
per-vendor presets. Each entry adds a button to the login page.

```yaml
server:
  base_url: https://placard.example.com

auth:
  registration_open: false
  oidc:
    - name: corp
      display_name: Company SSO
      issuer: https://sso.example.com/realms/main
      client_id: placard
      client_secret: ${OIDC_CLIENT_SECRET}
      scopes: [openid, profile, email]
```

Register `https://placard.example.com/auth/oidc/callback` as the redirect URI
with the provider. `name` is stored on every linked identity, so renaming it
later orphans them.

`auth.oidc` holds objects rather than strings, so it is the one key no
environment variable reaches: an SSO instance needs a config file. Under Docker,
drop it at `/data/config.yaml` and the server picks it up on its own.

> **Keep local registration closed on an SSO instance.** An SSO login whose
> provider reports `email_verified` is linked to an existing account with the
> same address, and Placard does not verify addresses typed into local
> registration.

### Database and storage

| | Default | Alternative |
| --- | --- | --- |
| Database | SQLite at `<data_dir>/placard.db` | Postgres (`database.driver: postgres` + a DSN) |
| Objects | a directory at `<data_dir>/objects` | AWS S3, Cloudflare R2 or MinIO (`storage.type: s3`) |
| Sessions, rate limits, cron lock | in the database and in-process | Redis (`redis.addr`) |

Schema migrations run at startup, so an upgrade needs no manual SQL.

**Redis is required to run more than one replica.** Without it each replica
keeps its own sessions, its own rate-limit budget and its own copy of the
cleanup cron — a login would only work on the replica that issued it.

## Administration

The first account on a fresh instance is the administrator, whatever
`auth.registration_open` says. From `/admin` an administrator can open or close
registration, toggle OIDC auto-provisioning, change the upload size limit, and
grant, withdraw or disable accounts.

When nobody can sign in at all — a forgotten password, a withdrawn admin flag,
an identity provider that has gone away — the server binary carries a recovery
subcommand that talks to the database directly:

```bash
placard-server admin user list
placard-server admin user create --username alice --email alice@example.com --admin
placard-server admin user set-admin --username alice
placard-server admin user reset-password --username alice
```

## Development

```bash
make frontend-install frontend   # npm ci, then the bundle the server embeds
make run                # run locally, SQLite under ./data
make gates              # build + vet + gofmt + unit tests — what CI runs
make up                 # Postgres + Redis for the integration suite
make test-integration   # the same tests against Postgres and Redis
```

Unit tests need nothing external: every test that touches the database gets its
own private SQLite one.

The Go module path is lowercase — `github.com/Xm798/placard` — while the
repository is `Xm798/Placard`. Import paths and `go install` must use the
lowercase spelling:

```bash
go install github.com/Xm798/placard/cmd/placard@latest
```

## License

[MIT](LICENSE)
