package app

import (
	"log/slog"
	"time"
)

const (
	defaultPostgresPort           = "5432"
	defaultPgDumpCompressionLevel = 6
	defaultAgeWorkFactor          = 18
	backupTimestampLayout         = "2006-01-02T15:04:05"
	backupFilenameTimestampLayout = "2006-01-02T15:04:05.000000000"
	maxBackupKeepDays             = 100000 // bounded to prevent time.Duration overflow
)

type addressingMode string

const (
	addressingAuto    addressingMode = "auto"
	addressingPath    addressingMode = "path"
	addressingVirtual addressingMode = "virtual"
)

type config struct {
	postgresDatabase       string
	postgresHost           string
	postgresPort           string
	postgresUser           string
	postgresPassword       string
	pgDumpExtraOpts        []string
	pgDumpCompressionLevel int
	pgRestoreExtraOpts     []string
	pgRestoreClean         bool
	s3AccessKeyID          string
	s3SecretAccessKey      string
	s3SessionToken         string
	s3Bucket               string
	s3Region               string
	s3Prefix               string
	s3Endpoint             string
	s3AddressingMode       addressingMode
	schedule               string
	passphrase             string
	agePublicKey           string // X25519 public key; used instead of passphrase when set
	ageIdentity            string // X25519 private identity; only used for restore
	ageWorkFactor          int    // scrypt work factor (default 18; only used with passphrase)
	backupKeepDays         int
	restoreTimestamp       string
	mode                   string // "backup" (default) or "restore"
	logLevel               slog.Level
}

type backupObject struct {
	key string
}

type backupInfo struct {
	key       string
	timestamp string
	createdAt time.Time
	encrypted bool
}
