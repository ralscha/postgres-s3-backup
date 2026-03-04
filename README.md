# postgres-s3-backup

Docker sidecar and CLI tool for backing up PostgreSQL databases directly to S3-compatible storage, with optional encryption and scheduling.

## Features

- Backup PostgreSQL to S3 or any S3-compatible storage (MinIO, Cloudflare R2, etc.)
- Restore latest backup or a specific backup by timestamp
- **Encryption options**:
  - Symmetric passphrase encryption via `PASSPHRASE` (age scrypt)
  - Asymmetric X25519 encryption via `AGE_PUBLIC_KEY` — backup host only needs the public key
- Optional retention cleanup via `BACKUP_KEEP_DAYS`
- Schedule support: `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`, or any Go duration like `24h`
- Configurable S3 addressing mode for compatibility (`auto`, `path`, `virtual`)


## Environment variables

### Required

| Variable            | Description                                                 |
|---------------------|-------------------------------------------------------------|
| `S3_BUCKET`         | S3 bucket name                                              |
| `S3_REGION`         | AWS/S3-compatible region (e.g. `us-east-1`, `auto` for R2) |
| `POSTGRES_DATABASE` | Database name to back up or restore                         |
| `POSTGRES_HOST`     | Postgres host                                               |
| `POSTGRES_USER`     | Postgres username                                           |

### Postgres

| Variable            | Default  | Description                                                                            |
|---------------------|----------|----------------------------------------------------------------------------------------|
| `POSTGRES_PASSWORD` | _(empty)_ | Postgres password. Omit when using `.pgpass`, `pg_service.conf`, or `trust`/`peer` auth |
| `POSTGRES_PORT`     | `5432`   | Postgres port                                                                          |
| `PGDUMP_EXTRA_OPTS` | _(empty)_ | Extra flags passed verbatim to `pg_dump`                                               |
| `PGDUMP_COMPRESS_LEVEL` | `6`  | Dump compression level `0`–`9`; ignored when `PGDUMP_EXTRA_OPTS` already sets `--compress`/`-Z` |

### S3

| Variable               | Default      | Description                                                          |
|------------------------|--------------|----------------------------------------------------------------------|
| `S3_ACCESS_KEY_ID`     | _(empty)_    | Access key (omit to use instance role / environment credentials)     |
| `S3_SECRET_ACCESS_KEY` | _(empty)_    | Secret key                                                           |
| `S3_ENDPOINT`          | _(empty)_    | Override endpoint URL for S3-compatible stores (e.g. MinIO)          |
| `S3_PREFIX`            | _(empty)_    | Object key prefix (e.g. `backup`). Empty = store at bucket root.     |
| `S3_ADDRESSING_MODE`   | `path`       | `auto`, `path`, or `virtual`                                         |

### Encryption

`PASSPHRASE` and `AGE_PUBLIC_KEY` are **mutually exclusive**. Leave both empty to disable encryption.

| Variable          | Description                                                                                                   |
|-------------------|---------------------------------------------------------------------------------------------------------------|
| `PASSPHRASE`      | Symmetric scrypt passphrase for encrypt/decrypt. **Must match** on backup and restore hosts.                  |
| `AGE_PUBLIC_KEY`  | X25519 public key (e.g. `age1...`). Backup requires only this; for **restore**, put the private key identity in `PASSPHRASE`. |
| `AGE_WORK_FACTOR` | scrypt work factor used with `PASSPHRASE` (default `18`; valid `1`–`30`). Higher = slower but stronger KDF.   |

#### Generating an X25519 key pair

```sh
# Install age: https://github.com/FiloSottile/age
age-keygen -o backup.key
# Public key is printed to stdout and stored in backup.key
```

Set `AGE_PUBLIC_KEY` to the public key (`age1...`) for backup.  
For restore, set `PASSPHRASE` to the full contents of `backup.key` (the identity file).

### Scheduling & mode

| Variable           | Default   | Description                                                                               |
|--------------------|-----------|-------------------------------------------------------------------------------------------|
| `SCHEDULE`         | _(empty)_ | Run interval. Empty = run once and exit. Examples: `@daily`, `@weekly`, `12h`             |
| `BACKUP_KEEP_DAYS` | `0`       | Delete backups older than N days. `0` disables pruning.                                   |
| `MODE`             | `backup`  | `backup`, `restore`, or `list`                                                            |
| `RESTORE_TIMESTAMP`| _(empty)_ | Specific backup timestamp to restore (`2026-01-14T12:00:00`). Empty = restore latest.     |
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

**Restore specific timestamp:**

```sh
docker run --rm --env-file .env -e MODE=restore -e RESTORE_TIMESTAMP=2026-01-14T12:00:00 ghcr.io/ralscha/postgres-s3-backup:latest
```

**List available backup timestamps:**

```sh
docker run --rm --env-file .env -e MODE=list ghcr.io/ralscha/postgres-s3-backup:latest
```

Outputs one timestamp per line (oldest first), suitable for use with `RESTORE_TIMESTAMP`.

