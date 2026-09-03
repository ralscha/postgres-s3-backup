package app

import (
	"reflect"
	"testing"

	"filippo.io/age"
)

var configEnvironment = []string{
	"MODE", "S3_BUCKET", "S3_REGION", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_SESSION_TOKEN",
	"S3_PREFIX", "S3_ENDPOINT", "S3_ADDRESSING_MODE", "POSTGRES_DATABASE",
	"POSTGRES_HOST", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_PORT",
	"PGDUMP_EXTRA_OPTS", "PGDUMP_COMPRESS_LEVEL", "PGRESTORE_EXTRA_OPTS",
	"PGRESTORE_CLEAN", "SCHEDULE", "PASSPHRASE", "AGE_PUBLIC_KEY",
	"AGE_IDENTITY", "AGE_WORK_FACTOR", "BACKUP_KEEP_DAYS", "RESTORE_TIMESTAMP",
	"LOG_LEVEL",
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	for _, name := range configEnvironment {
		t.Setenv(name, "")
	}
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("S3_REGION", "us-east-1")
	t.Setenv("POSTGRES_DATABASE", "appdb")
	t.Setenv("POSTGRES_HOST", "postgres")
	t.Setenv("POSTGRES_USER", "postgres")
}

func TestLoadConfigRejectsBackupWithPublicKeyAndPassphrase(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AGE_PUBLIC_KEY", "age1example")
	t.Setenv("PASSPHRASE", "secret")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected AGE_PUBLIC_KEY and PASSPHRASE to be rejected for backup mode")
	}
}

func TestLoadConfigAllowsRestoreIdentityWithPublicKeyMode(t *testing.T) {
	setRequiredEnv(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODE", "restore")
	t.Setenv("AGE_PUBLIC_KEY", identity.Recipient().String())
	t.Setenv("PASSPHRASE", identity.String())

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.mode != "restore" {
		t.Fatalf("mode = %q, want restore", cfg.mode)
	}
}

func TestLoadConfigAllowsRestoreWithDedicatedIdentity(t *testing.T) {
	setRequiredEnv(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODE", "restore")
	t.Setenv("AGE_IDENTITY", identity.String())

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.ageIdentity != identity.String() {
		t.Fatalf("ageIdentity = %q, want generated identity", cfg.ageIdentity)
	}
}

func TestLoadConfigRejectsPartialS3Credentials(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("S3_ACCESS_KEY_ID", "access-key")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected partial S3 credentials to be rejected")
	}
}

func TestLoadConfigListDoesNotRequirePostgresConnection(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MODE", "list")
	t.Setenv("POSTGRES_HOST", "")
	t.Setenv("POSTGRES_USER", "")

	if _, err := loadConfig(); err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
}

func TestLoadConfigDefaultsToCleanRestore(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MODE", "restore")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if !cfg.pgRestoreClean {
		t.Fatal("pgRestoreClean = false, want true")
	}
}

func TestLoadConfigRejectsInvalidRestoreTimestamp(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("MODE", "restore")
	t.Setenv("RESTORE_TIMESTAMP", "../../another-object")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected invalid RESTORE_TIMESTAMP to be rejected")
	}
}

func TestLoadConfigParsesQuotedExtraOptions(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PGDUMP_EXTRA_OPTS", `--exclude-table-data 'audit log' --no-owner`)
	t.Setenv("PGRESTORE_EXTRA_OPTS", `--role="restore user"`)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if want := []string{"--exclude-table-data", "audit log", "--no-owner"}; !reflect.DeepEqual(cfg.pgDumpExtraOpts, want) {
		t.Fatalf("pgDumpExtraOpts = %#v, want %#v", cfg.pgDumpExtraOpts, want)
	}
	if want := []string{"--role=restore user"}; !reflect.DeepEqual(cfg.pgRestoreExtraOpts, want) {
		t.Fatalf("pgRestoreExtraOpts = %#v, want %#v", cfg.pgRestoreExtraOpts, want)
	}
}

func TestSplitArgsRejectsUnterminatedQuote(t *testing.T) {
	if _, err := splitArgs(`--table "unfinished`); err == nil {
		t.Fatal("expected unterminated quote to be rejected")
	}
}

func TestLoadConfigRejectsPgDumpOutputOverride(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PGDUMP_EXTRA_OPTS", "--file=/tmp/not-streamed.dump")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected output override to be rejected")
	}
}

func TestLoadConfigSupportsS3SessionToken(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("S3_ACCESS_KEY_ID", "access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("S3_SESSION_TOKEN", "token")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.s3SessionToken != "token" {
		t.Fatalf("s3SessionToken = %q, want token", cfg.s3SessionToken)
	}
}

func TestLoadConfigRejectsRetentionThatOverflowsDuration(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("BACKUP_KEEP_DAYS", "100001")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected excessive BACKUP_KEEP_DAYS to be rejected")
	}
}
