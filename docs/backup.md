# Backup and restore

A Placard instance is a database, a set of page objects, and one secret key.
A backup needs all three.

The commands below assume the stock [`docker-compose.yaml`](../docker-compose.yaml)
(service `placard`, volume `placard_data` mounted at `/data`) and are run from
the directory that holds it.

## What to back up

| Item | Where | Lost without it |
| ---- | ----- | --------------- |
| `secret.key` | `/data/secret.key`, or the value of `PLACARD_SERVER_SECRET_KEY` if you set one | Every personal access token stops working, and every share code stops decrypting, which turns code-protected pages open ([details](#if-you-lost-secretkey)). |
| Database | SQLite: `/data/placard.db` and its `-wal` and `-shm` companions, which can hold recent commits even after a clean stop. Postgres: the `postgres` database. | Accounts, page metadata, versions, tokens, audit log. Nothing else can rebuild it. |
| Page objects | Local storage: `/data/objects/`. S3: the bucket. | The HTML of every page and version. Pages whose object is missing cannot be served. |
| `config.yaml` | `/data/config.yaml` when you mount one | OIDC providers, which only the config file can define. Skip it if you never created one. |

Storage shapes:

| Setup | Back up |
| ----- | ------- |
| Default (SQLite + local storage) | the whole `/data` volume, minus `logs/` |
| Postgres + local storage | `pg_dump`, plus `objects/`, `secret.key`, `config.yaml` from `/data` |
| Postgres or SQLite + S3 | the database, plus `secret.key` and `config.yaml`; the bucket by its own means |

`secret.key` is created with mode `0600` on first start. It is the pepper for
token hashes and the encryption key for stored share codes, so a backup that
holds it next to the database is as sensitive as the database itself. Encrypt
the archive, or keep the key in a separate secret store and set it with
`PLACARD_SERVER_SECRET_KEY` instead.

## What does not need a backup

- `/data/logs/`: the server also writes the same lines to stdout.
- Redis: it holds only login sessions, in-flight CLI device logins, rate-limit
  counters and the cleanup lock, all of which expire on their own. After a
  restore without Redis contents, signed-in browsers are asked to log in again
  and running `placard login` flows must be restarted. Without Redis configured
  these live in the database instead, and sessions in the backup stay valid
  until they expire.
- The image and the frontend bundle: pull them again.

## Procedure A: cold backup

Stop the app, archive the volume, start the app. The result is always
consistent. The image has no shell, so a throwaway `alpine` container does the
archiving.

```bash
mkdir -p backups
VOL=$(docker inspect "$(docker compose ps -aq placard)" \
  --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')
echo "$VOL"      # compose prefixes the volume name with the project name

docker compose stop placard
docker run --rm -v "$VOL":/data:ro -v "$PWD/backups":/backup alpine \
  tar czf /backup/placard-cold.tar.gz -C /data --exclude=./logs .
docker compose start placard
```

The archive keeps the owner (uid 65532) and modes of every file.

## Procedure B: online backup

No downtime. Copying `placard.db` while the server runs is not safe: SQLite is
in WAL mode, so recent commits live in `placard.db-wal` and a plain copy is
stale or torn. The `sqlite3` `.backup` command takes a consistent snapshot
instead.

```bash
mkdir -p backups
VOL=$(docker inspect "$(docker compose ps -aq placard)" \
  --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')

docker run --rm -v "$VOL":/data -v "$PWD/backups":/backup alpine sh -c '
  set -e
  apk add --no-cache -q sqlite
  mkdir /stage
  sqlite3 -readonly /data/placard.db ".backup /stage/placard.db"
  for f in objects secret.key config.yaml; do [ -e /data/$f ] && ln -s /data/$f /stage/$f; done
  tar czhf /backup/placard-online.tar.gz -C /stage .
'
```

The snapshot is taken first and `objects/` second. A publish uploads its
object before it commits the database row, so an object that is newer than the
snapshot is an unreferenced file, which is harmless. The other order can
capture a row whose object was never copied, and that page is broken.

The helper opens the database read-only and writes only into its own `/stage`
directory and the backup directory, so nothing root-owned lands in the volume.
The archive has the same layout as Procedure A and restores the same way; the
restore step sets ownership.

## Postgres

```bash
mkdir -p backups
docker compose exec -T postgres pg_dump -U placard -Fc placard > backups/placard.pgdump

VOL=$(docker inspect "$(docker compose ps -aq placard)" \
  --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')
docker run --rm -v "$VOL":/data:ro -v "$PWD/backups":/backup alpine sh -c '
  cd /data
  files="secret.key"; [ -d objects ] && files="$files objects"; [ -f config.yaml ] && files="$files config.yaml"
  tar czf /backup/placard-files.tar.gz $files
'
```

Take the dump first, then the files. Restore the database with the app stopped:

```bash
docker compose stop placard
docker compose exec -T postgres pg_restore -U placard -d placard --clean --if-exists < backups/placard.pgdump
```

Then follow the [restore steps](#restore), extracting `placard-files.tar.gz`
in step 2, and skip the database unpack.

## S3 storage

Turn on bucket versioning or cross-region replication at the provider; each page
version has its own object key, so a versioned or replicated bucket is a complete
copy of the pages. The database and `secret.key` still need the backups above.
Restore them, then point the server at the bucket.

## Restore

1. Stop the stack and empty the volume. Removing the volume lets Docker create
   a new one with the image's ownership (uid:gid 65532):

   ```bash
   VOL=$(docker inspect "$(docker compose ps -aq placard)" \
     --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')
   docker compose down
   docker volume rm "$VOL"
   docker compose up --no-start
   ```

2. Unpack the archive into the empty volume and fix ownership:

   ```bash
   docker run --rm -v "$VOL":/data -v "$PWD/backups":/backup:ro alpine sh -c '
     set -e
     tar xzf /backup/placard-online.tar.gz -C /data
     chown -R 65532:65532 /data
     chmod 600 /data/secret.key
   '
   ```

   Use the archive you have (`placard-cold.tar.gz` for Procedure A). If you
   keep the key in `PLACARD_SERVER_SECRET_KEY`, make sure the compose file
   still carries the same value.

3. Start the server: `docker compose up -d`. Pending schema migrations run at
   boot.

4. Verify:
   - `curl -i http://localhost:8080/api/ready` returns `200`.
   - You can log in to the web UI.
   - An existing `/s/<id>` link opens the same content.
   - `placard ls` with an existing CLI token still lists your pages.
   - A page protected by a share code still asks for its code and accepts the
     original one.

## If you lost secret.key

Starting with the database and objects but no `secret.key` (and no
`PLACARD_SERVER_SECRET_KEY`) makes the server generate a new key. Effects:

- **Still works:** login with existing passwords (argon2id, unrelated to the
  key), existing browser sessions, all pages without a share code, page
  contents and versions.
- **Personal access tokens stop working.** Every request with an old token
  gets `401`. Each user runs `placard login` again, or creates a new token in
  the web UI.
- **Share-code pages become open.** The stored code no longer decrypts, so the
  server logs `stored share code does not decrypt; serving the page as
  uncoded` and serves the page to anyone with the link. The owner's file list
  gives no sign of this. Treat every page that had a code as exposed until its
  owner sets a new code (`POST /api/files/<id>/share-code`, or the share dialog,
  or `--password auto` on a republish) or makes the page private.
  Unlock cookies issued earlier stop verifying as well.

An older copy of the key, from any backup of the same instance, is the fix:
put it back at `/data/secret.key` (mode `0600`, owner 65532) and restart.

## Versions

The server applies pending schema migrations at boot (`database.auto_migrate`,
on by default), so a backup from an older release restores into a newer
binary. A newer database in an older binary is unsupported: the migration tool
accepts the unknown schema, so the server may start and then misbehave.
Restore into the same or a newer release than the one that wrote the backup.
