# postgres-s3-backup

Docker sidecar and CLI tool for backing up PostgreSQL databases directly to S3-compatible storage, with optional encryption and scheduling.

## Features

- Backup PostgreSQL to S3 or any S3-compatible storage (MinIO, Cloudflare R2, etc.)
- Restore latest backup or a specific backup by timestamp
- **Encryption options**:
  - Symmetric passphrase encryption via `PASSPHRASE` (age scrypt)
  - Asymmetric X25519 encryption via `AGE_PUBLIC_KEY`; restore uses `AGE_IDENTITY`
- Optional retention cleanup via `BACKUP_KEEP_DAYS` for the configured database
- Schedule support: standard cron descriptors, Go durations like `24h`, or cron expressions like `0 2 * * *`
- Configurable S3 addressing mode for compatibility (`auto`, `path`, `virtual`)


## Environment variables

### Required

| Variable            | Description                                                 |
|---------------------|-------------------------------------------------------------|
| `S3_BUCKET`         | S3 bucket name                                              |
| `S3_REGION`         | AWS/S3-compatible region (e.g. `us-east-1`, `auto` for R2) |
| `POSTGRES_DATABASE` | Database name to back up or restore                         |
| `POSTGRES_HOST`     | Postgres host (required for backup and restore)              |
| `POSTGRES_USER`     | Postgres username (required for backup and restore)          |

### Postgres

| Variable            | Default  | Description                                                                            |
|---------------------|----------|----------------------------------------------------------------------------------------|
| `POSTGRES_PASSWORD` | _(empty)_ | Postgres password. Omit when using `.pgpass`, `pg_service.conf`, or `trust`/`peer` auth |
| `POSTGRES_PORT`     | `5432`   | Postgres port                                                                          |
| `PGDUMP_EXTRA_OPTS` | _(empty)_ | Extra flags passed to `pg_dump`; shell-style single/double quoting is supported         |
| `PGDUMP_COMPRESS_LEVEL` | `6`  | Dump compression level `0`-`9`; ignored when `PGDUMP_EXTRA_OPTS` already sets `--compress`/`-Z` |
| `PGRESTORE_EXTRA_OPTS` | _(empty)_ | Extra flags passed to `pg_restore`; shell-style single/double quoting is supported    |
| `PGRESTORE_CLEAN`   | `true`   | Remove existing database objects before recreating them during restore                  |

### S3

| Variable               | Default      | Description                                                          |
|------------------------|--------------|----------------------------------------------------------------------|
| `S3_ACCESS_KEY_ID`     | _(empty)_    | Access key (omit to use instance role / environment credentials)     |
| `S3_SECRET_ACCESS_KEY` | _(empty)_    | Secret key                                                           |
| `S3_SESSION_TOKEN`     | _(empty)_    | Optional session token for temporary static credentials              |
| `S3_ENDPOINT`          | _(empty)_    | Override endpoint URL for S3-compatible stores; omitted scheme defaults to HTTPS |
| `S3_PREFIX`            | _(empty)_    | Object key prefix (e.g. `backup`). Empty = store at bucket root.     |
| `S3_ADDRESSING_MODE`   | `path`       | `auto`, `path`, or `virtual`                                         |

### Encryption

For backups, `PASSPHRASE` and `AGE_PUBLIC_KEY` are **mutually exclusive**. Leave both empty to disable encryption.

| Variable          | Description                                                                                                   |
|-------------------|---------------------------------------------------------------------------------------------------------------|
| `PASSPHRASE`      | Symmetric scrypt passphrase for encrypt/decrypt. **Must match** on backup and restore hosts.                |
| `AGE_PUBLIC_KEY`  | X25519 public key (e.g. `age1...`) used for backup. The backup host only needs this public value.          |
| `AGE_IDENTITY`    | X25519 private identity (e.g. `AGE-SECRET-KEY-...`) used for restore. Keep this secret off the backup host. |
| `AGE_WORK_FACTOR` | scrypt work factor used with `PASSPHRASE` (default `18`; valid `1`-`30`). Higher = slower but stronger KDF. |

#### Generating an X25519 key pair

```sh
# Install age: https://github.com/FiloSottile/age
age-keygen -o backup.key
# Public key is printed to stdout and stored in backup.key
```

Set `AGE_PUBLIC_KEY` to the public key (`age1...`) for backup. For restore, set
`AGE_IDENTITY` to the `AGE-SECRET-KEY-...` line from `backup.key` (or the full
identity-file contents when your environment supports multiline values).
`AGE_PUBLIC_KEY` is not required on the restore host. The older combination of
`AGE_PUBLIC_KEY` plus an identity in `PASSPHRASE` remains supported for compatibility.

### Scheduling & mode

| Variable           | Default   | Description                                                                               |
|--------------------|-----------|-------------------------------------------------------------------------------------------|
| `SCHEDULE`         | _(empty)_ | Empty = run once. Durations such as `12h` run immediately and repeat; `@daily`, `@every 12h`, and cron expressions run on their cron schedule. |
| `BACKUP_KEEP_DAYS` | `0`       | Delete backups for this database older than N days. `0` disables pruning.                 |
| `MODE`             | `backup`  | `backup`, `restore`, or `list`                                                            |
| `RESTORE_TIMESTAMP`| _(empty)_ | Specific backup timestamp to restore. Copy the exact value from list output. Empty = restore latest. |
| `LOG_LEVEL`        | `info`    | `debug`, `info`, `warn`, or `error`                                                       |

## Docker image

Pre-built images are available from the GitHub Container Registry:

```sh
docker pull ghcr.io/ralscha/postgres-s3-backup:latest
```

## Sidecar usage example

Copy `template.env` to `.env` and fill in values.

```yaml
services:
  postgres:
    image: postgres:18-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: user
      POSTGRES_PASSWORD: password
      POSTGRES_DB: mydb

  backup:
    image: ghcr.io/ralscha/postgres-s3-backup:latest
    env_file: .env
    environment:
      POSTGRES_HOST: postgres
      POSTGRES_DATABASE: mydb
      POSTGRES_USER: user
      POSTGRES_PASSWORD: password
      SCHEDULE: "@daily"
      BACKUP_KEEP_DAYS: "30"
    restart: unless-stopped
```

## Run as CLI tool

**Backup once:**

```sh
docker run --rm --env-file .env ghcr.io/ralscha/postgres-s3-backup:latest
```

**Scheduled backup** (set `SCHEDULE=@daily` in `.env`):

```sh
docker run --env-file .env ghcr.io/ralscha/postgres-s3-backup:latest
```

**Restore latest:**

```sh
docker run --rm --env-file .env -e MODE=restore ghcr.io/ralscha/postgres-s3-backup:latest
```

Restore uses `--clean --if-exists` by default. Set `PGRESTORE_CLEAN=false` for
a non-cleaning restore, and use `PGRESTORE_EXTRA_OPTS` for options such as
`--no-owner` or `--role=app`.

**Restore specific timestamp:**

```sh
docker run --rm --env-file .env -e MODE=restore -e RESTORE_TIMESTAMP=2026-01-14T12:00:00.123456789 ghcr.io/ralscha/postgres-s3-backup:latest
```

**List available backup timestamps:**

```sh
docker run --rm --env-file .env -e MODE=list ghcr.io/ralscha/postgres-s3-backup:latest
```

Outputs one timestamp per line (oldest first), suitable for use with `RESTORE_TIMESTAMP`.

