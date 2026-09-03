package app

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"filippo.io/age"
)

type fakeStorage struct {
	objects []backupObject
	deleted []string
}

func (*fakeStorage) uploadStream(context.Context, string, string, io.Reader) error { return nil }
func (*fakeStorage) downloadFile(context.Context, string, string, string) error    { return nil }
func (s *fakeStorage) listObjects(context.Context, string, string) ([]backupObject, error) {
	return s.objects, nil
}
func (s *fakeStorage) deleteObject(_ context.Context, _, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}

func TestBackupObjectPrefixIncludesDatabase(t *testing.T) {
	cfg := config{postgresDatabase: "appdb", s3Prefix: " backups/ "}

	got := backupObjectPrefix(cfg)
	want := "backups/appdb_"
	if got != want {
		t.Fatalf("backupObjectPrefix() = %q, want %q", got, want)
	}
}

func TestBuildPgDumpCmdUsesDefaultCompression(t *testing.T) {
	cfg := config{
		postgresHost:           "postgres",
		postgresPort:           "5432",
		postgresUser:           "postgres",
		postgresDatabase:       "appdb",
		pgDumpCompressionLevel: 6,
	}

	cmd := buildPgDumpCmd(context.Background(), cfg)
	want := []string{"--format=custom", "--compress", "6", "-h", "postgres", "-p", "5432", "-U", "postgres", "-d", "appdb"}
	if !reflect.DeepEqual(cmd.Args[1:], want) {
		t.Fatalf("pg_dump args = %#v, want %#v", cmd.Args[1:], want)
	}
}

func TestBuildPgDumpCmdLetsExtraCompressionOverrideDefault(t *testing.T) {
	cfg := config{
		postgresHost:           "postgres",
		postgresPort:           "5432",
		postgresUser:           "postgres",
		postgresDatabase:       "appdb",
		pgDumpCompressionLevel: 6,
		pgDumpExtraOpts:        []string{"--compress=9", "--no-owner"},
	}

	cmd := buildPgDumpCmd(context.Background(), cfg)
	for i, arg := range cmd.Args[1:] {
		if arg == "--compress" {
			t.Fatalf("default --compress found at arg %d in %#v", i+1, cmd.Args)
		}
	}
	wantSuffix := []string{"--compress=9", "--no-owner"}
	gotSuffix := cmd.Args[len(cmd.Args)-len(wantSuffix):]
	if !reflect.DeepEqual(gotSuffix, wantSuffix) {
		t.Fatalf("pg_dump arg suffix = %#v, want %#v", gotSuffix, wantSuffix)
	}
}

func TestBuildPgRestoreCmdSupportsNonDestructiveRestoreAndExtraOptions(t *testing.T) {
	cfg := config{
		postgresHost:       "postgres",
		postgresPort:       "5432",
		postgresUser:       "postgres",
		postgresDatabase:   "appdb",
		pgRestoreClean:     false,
		pgRestoreExtraOpts: []string{"--no-owner", "--role=app"},
	}

	cmd := buildPgRestoreCmd(context.Background(), cfg, "backup.dump")
	want := []string{"-h", "postgres", "-p", "5432", "-U", "postgres", "-d", "appdb", "--no-owner", "--role=app", "backup.dump"}
	if !reflect.DeepEqual(cmd.Args[1:], want) {
		t.Fatalf("pg_restore args = %#v, want %#v", cmd.Args[1:], want)
	}
}

func TestResolveBackupKeyUsesFilenameTimestampAndFiltersEncryption(t *testing.T) {
	cfg := config{postgresDatabase: "appdb", s3Bucket: "bucket", s3Prefix: "backups"}
	storage := &fakeStorage{objects: []backupObject{
		{key: "backups/appdb_2026-01-03T00:00:00.dump.age"},
		{key: "backups/appdb_not-a-timestamp.dump"},
		{key: "backups/appdb_2026-01-02T00:00:00.dump"},
		{key: "backups/appdb_2026-01-01T00:00:00.dump"},
	}}

	got, err := resolveBackupKey(context.Background(), cfg, storage, "", false)
	if err != nil {
		t.Fatalf("resolveBackupKey() error = %v", err)
	}
	want := "backups/appdb_2026-01-02T00:00:00.dump"
	if got != want {
		t.Fatalf("resolveBackupKey() = %q, want %q", got, want)
	}
}

func TestPruneOldBackupsOnlyDeletesValidExpiredBackups(t *testing.T) {
	cfg := config{postgresDatabase: "appdb", s3Bucket: "bucket", s3Prefix: "backups", backupKeepDays: 7}
	oldTimestamp := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(backupTimestampLayout)
	recentTimestamp := time.Now().UTC().Add(-6 * 24 * time.Hour).Format(backupTimestampLayout)
	oldKey := backupObjectKey(cfg, oldTimestamp, false)
	storage := &fakeStorage{objects: []backupObject{
		{key: oldKey},
		{key: backupObjectKey(cfg, recentTimestamp, false)},
		{key: "backups/appdb_notes.dump"},
		{key: "backups/appdb_2000-01-01T00:00:00.dump/child"},
	}}

	if err := pruneOldBackups(context.Background(), cfg, storage); err != nil {
		t.Fatalf("pruneOldBackups() error = %v", err)
	}
	if want := []string{oldKey}; !reflect.DeepEqual(storage.deleted, want) {
		t.Fatalf("deleted = %#v, want %#v", storage.deleted, want)
	}
}

func TestNormalizeS3Endpoint(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "adds scheme", input: "localhost:9000", want: "https://localhost:9000"},
		{name: "keeps http", input: "http://minio:9000/", want: "http://minio:9000"},
		{name: "rejects unsupported scheme", input: "ftp://example.com", wantErr: true},
		{name: "rejects query", input: "https://example.com?x=1", wantErr: true},
		{name: "rejects embedded user info", input: "https://user@example.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeS3Endpoint(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizeS3Endpoint() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("normalizeS3Endpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseBackupTimestampAcceptsFractionalSeconds(t *testing.T) {
	if _, err := parseBackupTimestamp("2026-01-02T03:04:05.123456789"); err != nil {
		t.Fatalf("parseBackupTimestamp() error = %v", err)
	}
}

func TestAgeEncryptionRoundTrips(t *testing.T) {
	firstIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	scryptPassphrase := firstIdentity.String()
	secondIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		backupCfg  config
		restoreCfg config
	}{
		{
			name:       "scrypt",
			backupCfg:  config{passphrase: scryptPassphrase, ageWorkFactor: 1},
			restoreCfg: config{passphrase: scryptPassphrase},
		},
		{
			name:      "X25519 identity list",
			backupCfg: config{agePublicKey: secondIdentity.Recipient().String()},
			restoreCfg: config{
				ageIdentity: firstIdentity.String() + "\n" + secondIdentity.String(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext := []byte("database backup contents")
			var encrypted bytes.Buffer
			recipient, err := buildAgeRecipient(tt.backupCfg)
			if err != nil {
				t.Fatal(err)
			}
			writer, err := age.Encrypt(&encrypted, recipient)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(plaintext); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}

			identities, err := buildAgeIdentities(tt.restoreCfg)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := age.Decrypt(bytes.NewReader(encrypted.Bytes()), identities...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plaintext) {
				t.Fatalf("decrypted content = %q, want %q", got, plaintext)
			}
		})
	}
}
