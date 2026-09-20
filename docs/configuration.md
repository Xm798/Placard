# Configuration

Placard reads its settings from three places, in increasing order of precedence:

1. built-in defaults — enough to start the server in an empty directory,
2. a YAML config file, and
3. environment variables.

Everything below is optional. A server started with no config file at all
creates `./data/`, opens a SQLite database in it, generates an instance secret
and listens on `:8080`.

## The config file

The file is looked for in this order:

1. `--config <path>` on the command line,
2. `$PLACARD_CONFIG`,
3. `config.yaml` in the working directory.

A path named through the flag or the environment must exist — a typo fails at
startup rather than quietly booting on defaults. `config.yaml` is used only if
it happens to be there.

[`config.example.yaml`](../config.example.yaml) is the annotated version of this
page; copy it to `config.yaml` and set only what you want to change.

## Environment variables

Every **scalar** key can be set as `PLACARD_` plus the key upper-cased with dots
replaced by underscores. `server.base_url` is `PLACARD_SERVER_BASE_URL`,
`storage.s3.bucket` is `PLACARD_STORAGE_S3_BUCKET`. An environment variable
always wins over the file.

The **string-list** keys — `server.trusted_proxies`, `csrf.allowed_origins` and
`cors.allowed_origins` — take a comma-separated value:

```
PLACARD_SERVER_TRUSTED_PROXIES=10.0.0.1,10.0.0.2
```

`auth.oidc` is the one key no environment variable reaches: its entries are
objects, not strings, and setting `PLACARD_AUTH_OIDC` fails at startup. An
instance configuring single sign-on therefore needs a config file.

String values in the file may reference the environment as `${VAR}`, which is
how credentials stay out of it:

```yaml
storage:
  s3:
    secret_key: ${S3_SECRET_KEY}
```

Those references name whatever variable you chose, without the `PLACARD_`
prefix — the prefix applies only to the automatic key-to-variable mapping.

Two variables are read directly and are not config keys:

| Variable          | Default       | Meaning                                                                                              |
| ----------------- | ------------- | ---------------------------------------------------------------------------------------------------- |
| `PLACARD_CONFIG`  | —             | Path to the config file.                                                                             |
| `APP_ENV`         | `prod`        | Deployment environment. Only `local` permits `auth.dev_mock`; everything else is validated identically. |

## Paths

`data_dir` is the root every unset path derives from, so one volume mount
persists the whole instance:

| Derived path         | From                    |
| -------------------- | ----------------------- |
| `<data_dir>/placard.db` | `database.sqlite.path` when unset |
| `<data_dir>/objects`    | `storage.local.dir` when unset    |
| `<data_dir>/secret.key` | always                            |

`log.file` is **not** derived from `data_dir`; it defaults to the relative
`./logs/placard.log`, which under the official image (working directory `/data`)
lands in the volume anyway.

---

## `data_dir`

| Key        | Env                  | Default  |
| ---------- | -------------------- | -------- |
| `data_dir` | `PLACARD_DATA_DIR`   | `./data` |

Root of everything the instance writes.

## `server`

| Key                       | Env                              | Default                 |
| ------------------------- | -------------------------------- | ----------------------- |
| `server.port`             | `PLACARD_SERVER_PORT`            | `8080`                  |
| `server.base_url`         | `PLACARD_SERVER_BASE_URL`        | `http://localhost:8080` |
| `server.trusted_proxies`  | `PLACARD_SERVER_TRUSTED_PROXIES` | `[]`                    |
| `server.proxy_header`     | `PLACARD_SERVER_PROXY_HEADER`    | `X-Real-IP`             |
| `server.secret_key`       | `PLACARD_SERVER_SECRET_KEY`      | generated               |
| `server.min_cli_version`  | `PLACARD_SERVER_MIN_CLI_VERSION` | `""` (no check)         |
| `server.read_buffer_size` | `PLACARD_SERVER_READ_BUFFER_SIZE`| `16384`                 |

- **`base_url`** is the canonical public origin (absolute `http(s)` URL with a host, no path). Share links
  are built from it, the CSRF and CORS allowlists default to it, and the session
  cookie's name and `Secure` flag follow its scheme: over `https` the cookie is
  `__Host-placard_session`, over plain `http` (localhost development) it is
  `placard_session` without `Secure`. Behind a domain or a reverse proxy this
  MUST be the URL browsers use.
- **`trusted_proxies`** is required behind a reverse proxy. Without it every
  client resolves to the proxy's address and so shares one per-IP rate-limit
  bucket. `proxy_header` names the header the client address is read from.
- **`secret_key`** is the 32-byte hex instance secret every keyed primitive
  derives from. Left empty it is generated into `<data_dir>/secret.key` (mode
  `0600`) on the first start. Changing or losing it invalidates every personal
  access token.
- **`min_cli_version`** is the oldest `placard` CLI this instance answers, e.g.
  `1.2.0`. An older CLI gets HTTP 426 and an upgrade message instead of failing
  deeper in the request.

## `database`

| Key                       | Env                              | Default                   |
| ------------------------- | -------------------------------- | ------------------------- |
| `database.driver`         | `PLACARD_DATABASE_DRIVER`        | `sqlite`                  |
| `database.dsn`            | `PLACARD_DATABASE_DSN`           | `""`                      |
| `database.sqlite.path`    | `PLACARD_DATABASE_SQLITE_PATH`   | `<data_dir>/placard.db`   |
| `database.auto_migrate`   | `PLACARD_DATABASE_AUTO_MIGRATE`  | `true`                    |
| `database.max_open_conns` | `PLACARD_DATABASE_MAX_OPEN_CONNS`| `100`                     |
| `database.max_idle_conns` | `PLACARD_DATABASE_MAX_IDLE_CONNS`| `10`                      |

`driver` is `sqlite` or `postgres`. `dsn` is required by — and only read by —
the Postgres driver; SQLite is addressed by `sqlite.path`.

```yaml
database:
  driver: postgres
  dsn: host=postgres port=5432 user=placard password=placard dbname=placard sslmode=disable TimeZone=UTC
```

With `auto_migrate` on (the default) the schema is brought up to date at
startup, so an upgrade needs no manual SQL.

## `storage`

| Key                      | Env                             | Default                |
| ------------------------ | ------------------------------- | ---------------------- |
| `storage.type`           | `PLACARD_STORAGE_TYPE`          | `local`                |
| `storage.local.dir`      | `PLACARD_STORAGE_LOCAL_DIR`     | `<data_dir>/objects`   |
| `storage.s3.endpoint`    | `PLACARD_STORAGE_S3_ENDPOINT`   | `""`                   |
| `storage.s3.region`      | `PLACARD_STORAGE_S3_REGION`     | `""`                   |
| `storage.s3.bucket`      | `PLACARD_STORAGE_S3_BUCKET`     | `""`                   |
| `storage.s3.access_key`  | `PLACARD_STORAGE_S3_ACCESS_KEY` | `""`                   |
| `storage.s3.secret_key`  | `PLACARD_STORAGE_S3_SECRET_KEY` | `""`                   |
| `storage.s3.path_style`  | `PLACARD_STORAGE_S3_PATH_STYLE` | `false`                |

`type` is `local` (a directory on disk) or `s3` (any S3-compatible service).
With `s3`, bucket, region, access key and secret key are all required and
checked at startup rather than at the first publish.

Page bytes are always served through the server, so the bucket stays private —
no public bucket or CDN is needed.

```yaml
# Cloudflare R2
storage:
  type: s3
  s3:
    endpoint: https://<account>.r2.cloudflarestorage.com
    region: auto
    bucket: placard
    access_key: ${S3_ACCESS_KEY}
    secret_key: ${S3_SECRET_KEY}

# MinIO
storage:
  type: s3
  s3:
    endpoint: http://minio:9000
    region: us-east-1
    bucket: placard
    access_key: ${S3_ACCESS_KEY}
    secret_key: ${S3_SECRET_KEY}
    path_style: true
```

`endpoint` is empty for AWS S3 (the SDK derives it from the region), `region:
auto` is what R2 wants, and `path_style` is required by MinIO.

## `redis`

| Key              | Env                       | Default |
| ---------------- | ------------------------- | ------- |
| `redis.addr`     | `PLACARD_REDIS_ADDR`      | `""`    |
| `redis.password` | `PLACARD_REDIS_PASSWORD`  | `""`    |
| `redis.db`       | `PLACARD_REDIS_DB`        | `0`     |

Optional, and empty by default: sessions and CLI device flows then live in the
database, and the rate-limit counters and the cleanup lock live in the process.

Redis is **required to run more than one replica**. Without it each replica
keeps its own sessions (a login only works on the replica that issued it), its
own rate-limit budget, and runs its own copy of the cleanup cron.

## `auth`

| Key                          | Env                                 | Default           |
| ---------------------------- | ----------------------------------- | ----------------- |
| `auth.registration_open`     | `PLACARD_AUTH_REGISTRATION_OPEN`    | `false`           |
| `auth.oidc_auto_provision`   | `PLACARD_AUTH_OIDC_AUTO_PROVISION`  | `true`            |
| `auth.session.cookie_name`   | `PLACARD_AUTH_SESSION_COOKIE_NAME`  | `placard_session` |
| `auth.session.idle_ttl`      | `PLACARD_AUTH_SESSION_IDLE_TTL`     | `168h`            |
| `auth.session.absolute_ttl`  | `PLACARD_AUTH_SESSION_ABSOLUTE_TTL` | `720h`            |
| `auth.oidc`                  | — (config file only)                | `[]`              |

`registration_open` and `oidc_auto_provision` **seed the instance settings on
the first start only**. From then on the database is authoritative and an admin
edits them from `/admin`, so changing them in the file later has no effect.

An instance with no accounts always accepts its first registration and makes
that account the administrator, whatever `registration_open` says — otherwise a
fresh install could never reach the switch.

`idle_ttl` is sliding and refreshed on every request; `absolute_ttl` is a hard
cap measured from sign-in and must be at least `idle_ttl`.

### `auth.oidc`

A list of objects, so it can only be set in the config file. Every provider with an OpenID
Connect discovery document works; there are no per-vendor presets. Each entry
adds one button to the login page and one linkable identity on the settings page.

```yaml
auth:
  oidc:
    - name: corp
      display_name: Company SSO
      issuer: https://sso.example.com/realms/main
      client_id: placard
      client_secret: ${OIDC_CLIENT_SECRET}
      scopes: [openid, profile, email]
```

| Field           | Required | Notes                                                                     |
| --------------- | -------- | ------------------------------------------------------------------------- |
| `name`          | yes      | Stored on every linked identity. Renaming it orphans them. Must be unique, and must not be `local`. |
| `display_name`  | no       | The login button's label; falls back to `name`. Safe to change.           |
| `issuer`        | yes      | Discovery base URL, no `/.well-known` suffix — the value the provider also puts in the `iss` claim. |
| `client_id`     | yes      |                                                                           |
| `client_secret` | yes      |                                                                           |
| `scopes`        | no       | Defaults to `[openid, profile, email]`. `openid` is added whether listed or not. |

Register this exact redirect URI with the provider:

```
<server.base_url>/auth/oidc/callback
```

> **Keep registration closed on an SSO instance.** An SSO login whose provider
> reports `email_verified` is linked to an existing account with the same
> address. Placard does not verify addresses typed into local registration, so
> `registration_open: true` alongside SSO lets anyone register claiming an
> address they do not own and then receive that person's SSO login.

### `auth.dev_mock`

Injects a fixed identity ahead of every credential check. `config.Validate`
rejects it unless `APP_ENV=local`, so it can never be reached on a deployed
instance. Fields: `enabled`, `uid`, `user`, `email`, `display_name`.

## `upload`

| Key                     | Env                            | Default              |
| ----------------------- | ------------------------------ | -------------------- |
| `upload.max_file_size`  | `PLACARD_UPLOAD_MAX_FILE_SIZE` | `10485760` (10 MiB)  |

Seeds the setting table on a first start; admins change it from `/admin`
afterwards.

## `token`

| Key                    | Env                           | Default |
| ---------------------- | ----------------------------- | ------- |
| `token.max_ttl_days`   | `PLACARD_TOKEN_MAX_TTL_DAYS`  | `365`   |

Caps the lifetime of a personal access token created from the web UI. A longer
requested expiry — including "never" — is clamped to this many days from now.
The CLI's device-code login is not routed through it and issues a 180-day
credential.

## `avatar`

| Key                         | Env                                | Default |
| --------------------------- | ---------------------------------- | ------- |
| `avatar.gravatar_fallback`  | `PLACARD_AVATAR_GRAVATAR_FALLBACK` | `true`  |

An account with no provider picture falls back to the Gravatar for its email
address, and one with neither to its initial letter in the UI. Turning this off
stops the server from sending a hash of any user's address to Gravatar.

Avatars are always fetched server-side and served same-origin, so a visitor's
browser never talks to the provider's image host.

## `ratelimit`

| Key                                    | Env                                            | Default | Scope                    |
| -------------------------------------- | ---------------------------------------------- | ------- | ------------------------ |
| `ratelimit.uploads_per_hour`           | `PLACARD_RATELIMIT_UPLOADS_PER_HOUR`           | `50`    | per user                 |
| `ratelimit.auth_per_minute`            | `PLACARD_RATELIMIT_AUTH_PER_MINUTE`            | `30`    | per IP, every attempt    |
| `ratelimit.auth_fail_per_minute`       | `PLACARD_RATELIMIT_AUTH_FAIL_PER_MINUTE`       | `20`    | per IP, failures only    |
| `ratelimit.anon_render_per_minute`     | `PLACARD_RATELIMIT_ANON_RENDER_PER_MINUTE`     | `120`   | per IP, anonymous only   |
| `ratelimit.share_code_fail_per_minute` | `PLACARD_RATELIMIT_SHARE_CODE_FAIL_PER_MINUTE` | `10`    | per page per IP          |

`auth_per_minute` counts every attempt, not only the failures: each one runs an
argon2 verification (64 MiB, 3 passes) before it can know whether the password
was right.

Rendering one share page spends three of the `anon_render_per_minute` budget
(shell, meta, render), so the default leaves an anonymous visitor 40 page views
a minute. Signed-in visitors are exempt.

`share_code_fail_per_minute` — not the code's six digits — is what makes a share
code a secret. A share code raises the cost of a leaked link; it is not an
authentication factor, and a page that must not be read by the wrong person
belongs on visibility `private`.

## `log`

| Key               | Env                        | Default              |
| ----------------- | -------------------------- | -------------------- |
| `log.level`       | `PLACARD_LOG_LEVEL`        | `info`               |
| `log.file`        | `PLACARD_LOG_FILE`         | `./logs/placard.log` |
| `log.max_size`    | `PLACARD_LOG_MAX_SIZE`     | `100` (MB)           |
| `log.max_backups` | `PLACARD_LOG_MAX_BACKUPS`  | `5`                  |
| `log.max_age`     | `PLACARD_LOG_MAX_AGE`      | `30` (days)          |
| `log.compress`    | `PLACARD_LOG_COMPRESS`     | `true`               |

`level` is `debug`, `info`, `warn` or `error`. Logs always go to the console;
naming a `file` adds a rotated JSON copy at that path. The rotation settings
apply to that file only.

## `csrf` / `cors`

| Key                     | Env                             | Default             |
| ----------------------- | ------------------------------- | ------------------- |
| `csrf.allowed_origins`  | `PLACARD_CSRF_ALLOWED_ORIGINS`  | `[server.base_url]` |
| `cors.allowed_origins`  | `PLACARD_CORS_ALLOWED_ORIGINS`  | `[server.base_url]` |

Both derive from `server.base_url` when unset, which is what makes setting
`base_url` alone sufficient. Set them explicitly only when the UI is served from
more than one origin — an allowlist that does not contain the UI's origin makes
every cookie-channel write fail with 403.

## `cleanup`

| Key                                 | Env                                          | Default |
| ----------------------------------- | -------------------------------------------- | ------- |
| `cleanup.enabled`                   | `PLACARD_CLEANUP_ENABLED`                    | `true`  |
| `cleanup.interval_seconds`          | `PLACARD_CLEANUP_INTERVAL_SECONDS`           | `3600`  |
| `cleanup.lock_ttl_seconds`          | `PLACARD_CLEANUP_LOCK_TTL_SECONDS`           | `300`   |
| `cleanup.retry_max`                 | `PLACARD_CLEANUP_RETRY_MAX`                  | `5`     |
| `cleanup.view_recompute_every`      | `PLACARD_CLEANUP_VIEW_RECOMPUTE_EVERY`       | `24`    |
| `cleanup.user_delete_retention_days`| `PLACARD_CLEANUP_USER_DELETE_RETENTION_DAYS` | `0`     |

The in-process hourly cron: reclaim expired pages, reconcile pending object
deletions, purge expired sessions and device codes, periodically recompute view
counts, and — on `local` storage — sweep staging files a killed process left in
the object directory. `view_recompute_every` is counted in rounds, and `0`
disables that step. `lock_ttl_seconds` must exceed the worst-case duration of one
round.

The staging sweep has no settings: it runs every 24 rounds and only removes
files untouched for 24 hours, a margin that keeps it clear of uploads still
streaming in.

Serialization is by Redis when `redis.addr` is set and in-process otherwise, so
a single-node deployment cleans up without Redis.

## Validation

Configuration is checked at startup and the server refuses to start on a bad
value rather than failing at the first request. The rules:

- `server.port` in `1..65535`
- `database.driver` is `sqlite` or `postgres`; `postgres` requires `database.dsn`
- `storage.type` is `local` or `s3`; `s3` requires bucket, region, access key and secret key
- `server.min_cli_version`, when set, is a release version such as `1.2.0`
- `auth.session.idle_ttl > 0` and `absolute_ttl >= idle_ttl`
- every `auth.oidc` entry has a unique `name` (never `local`), an `issuer`, a `client_id` and a `client_secret`
- `cleanup.interval_seconds` and `lock_ttl_seconds` are positive, `user_delete_retention_days` is not negative
- `auth.dev_mock.enabled` is false unless `APP_ENV=local`

A `${VAR}` that expands to nothing fails the corresponding non-empty check, so a
missing credential is caught at startup too.
