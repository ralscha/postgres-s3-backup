package app

import (
	"reflect"
	"testing"
)

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

	cmd := buildPgDumpCmd(cfg)
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

	cmd := buildPgDumpCmd(cfg)
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
