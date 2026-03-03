package app

import (
	"log/slog"
	"time"
)

const (
	defaultPostgresPort           = "5432"
	defaultPgDumpCompressionLevel = 6
	defaultAgeWorkFactor          = 18
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
	s3AccessKeyID          string
	s3SecretAccessKey      string
	s3Bucket               string
	s3Region               string
	s3Prefix               string
	s3Endpoint             string
	s3AddressingMode       addressingMode
	schedule               string
	passphrase             string
	agePublicKey           string // X25519 public key; used instead of passphrase when set
	ageWorkFactor          int    // scrypt work factor (default 18; only used with passphrase)
	backupKeepDays         int
	restoreTimestamp       string
	mode                   string // "backup" (default) or "restore"
	logLevel               slog.Level
}

type backupObject struct {
	key          string
	lastModified time.Time
}
