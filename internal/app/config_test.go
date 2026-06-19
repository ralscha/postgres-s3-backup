package app

import "testing"

func setRequiredEnv(t *testing.T) {
	t.Helper()
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
	t.Setenv("MODE", "restore")
	t.Setenv("AGE_PUBLIC_KEY", "age1example")
	t.Setenv("PASSPHRASE", "AGE-SECRET-KEY-example")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.mode != "restore" {
		t.Fatalf("mode = %q, want restore", cfg.mode)
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
