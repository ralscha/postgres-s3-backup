package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
)

func loadConfig() (config, error) {
	var cfg config

	rawMode := strings.ToLower(strings.TrimSpace(os.Getenv("MODE")))
	if rawMode == "" {
		rawMode = "backup"
	}
	if rawMode != "backup" && rawMode != "restore" && rawMode != "list" {
		return cfg, fmt.Errorf("invalid MODE %q (allowed: backup, restore, list)", rawMode)
	}
	cfg.mode = rawMode

	cfg.s3Bucket = strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if cfg.s3Bucket == "" {
		return cfg, errors.New("you need to set the S3_BUCKET environment variable")
	}

	cfg.postgresDatabase = strings.TrimSpace(os.Getenv("POSTGRES_DATABASE"))
	if cfg.postgresDatabase == "" {
		return cfg, errors.New("you need to set the POSTGRES_DATABASE environment variable")
	}

	if strings.ContainsAny(cfg.postgresDatabase, "/\\\r\n") {
		return cfg, errors.New("POSTGRES_DATABASE cannot contain slashes or newlines")
	}

	if cfg.mode != "list" {
		cfg.postgresHost = strings.TrimSpace(os.Getenv("POSTGRES_HOST"))
		if cfg.postgresHost == "" {
			return cfg, errors.New("you need to set the POSTGRES_HOST environment variable")
		}

		cfg.postgresUser = strings.TrimSpace(os.Getenv("POSTGRES_USER"))
		if cfg.postgresUser == "" {
			return cfg, errors.New("you need to set the POSTGRES_USER environment variable")
		}
	}

	cfg.postgresPassword = os.Getenv("POSTGRES_PASSWORD")

	if cfg.postgresPort == "" {
		cfg.postgresPort = strings.TrimSpace(os.Getenv("POSTGRES_PORT"))
	}
	if cfg.postgresPort == "" {
		cfg.postgresPort = defaultPostgresPort
	}
	port, err := strconv.Atoi(cfg.postgresPort)
	if err != nil || port < 1 || port > 65535 {
		return cfg, fmt.Errorf("invalid POSTGRES_PORT value %q (allowed: 1-65535)", cfg.postgresPort)
	}

	cfg.pgDumpExtraOpts, err = splitArgs(strings.TrimSpace(os.Getenv("PGDUMP_EXTRA_OPTS")))
	if err != nil {
		return cfg, fmt.Errorf("invalid PGDUMP_EXTRA_OPTS: %w", err)
	}
	if err := validatePgDumpExtraOpts(cfg.pgDumpExtraOpts); err != nil {
		return cfg, err
	}
	cfg.pgDumpCompressionLevel = defaultPgDumpCompressionLevel
	if raw := strings.TrimSpace(os.Getenv("PGDUMP_COMPRESS_LEVEL")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 0 || value > 9 {
			return cfg, fmt.Errorf("invalid PGDUMP_COMPRESS_LEVEL value %q (allowed: 0-9)", raw)
		}
		cfg.pgDumpCompressionLevel = value
	}
	cfg.pgRestoreExtraOpts, err = splitArgs(strings.TrimSpace(os.Getenv("PGRESTORE_EXTRA_OPTS")))
	if err != nil {
		return cfg, fmt.Errorf("invalid PGRESTORE_EXTRA_OPTS: %w", err)
	}
	cfg.pgRestoreClean = true
	if raw := strings.TrimSpace(os.Getenv("PGRESTORE_CLEAN")); raw != "" {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return cfg, fmt.Errorf("invalid PGRESTORE_CLEAN value %q (allowed: true,false)", raw)
		}
		cfg.pgRestoreClean = value
	}
	cfg.s3AccessKeyID = strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID"))
	cfg.s3SecretAccessKey = strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY"))
	cfg.s3SessionToken = strings.TrimSpace(os.Getenv("S3_SESSION_TOKEN"))
	if (cfg.s3AccessKeyID == "") != (cfg.s3SecretAccessKey == "") {
		return cfg, errors.New("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together")
	}
	if cfg.s3SessionToken != "" && cfg.s3AccessKeyID == "" {
		return cfg, errors.New("S3_SESSION_TOKEN requires S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY")
	}
	cfg.s3Region = strings.TrimSpace(os.Getenv("S3_REGION"))
	if cfg.s3Region == "" {
		return cfg, errors.New("you need to set the S3_REGION environment variable")
	}

	cfg.s3Prefix = strings.Trim(strings.TrimSpace(os.Getenv("S3_PREFIX")), "/")

	cfg.s3Endpoint, err = normalizeS3Endpoint(os.Getenv("S3_ENDPOINT"))
	if err != nil {
		return cfg, err
	}
	cfg.schedule = strings.TrimSpace(os.Getenv("SCHEDULE"))
	cfg.passphrase = os.Getenv("PASSPHRASE")
	cfg.agePublicKey = strings.TrimSpace(os.Getenv("AGE_PUBLIC_KEY"))
	cfg.ageIdentity = strings.TrimSpace(os.Getenv("AGE_IDENTITY"))

	cfg.ageWorkFactor = defaultAgeWorkFactor
	if raw := strings.TrimSpace(os.Getenv("AGE_WORK_FACTOR")); raw != "" {
		wf, parseErr := strconv.Atoi(raw)
		if parseErr != nil || wf < 1 || wf > 30 {
			return cfg, fmt.Errorf("invalid AGE_WORK_FACTOR value %q (allowed: 1-30)", raw)
		}
		cfg.ageWorkFactor = wf
	}

	cfg.restoreTimestamp = strings.TrimSpace(os.Getenv("RESTORE_TIMESTAMP"))
	if cfg.restoreTimestamp != "" {
		if cfg.mode != "restore" {
			return cfg, errors.New("RESTORE_TIMESTAMP is only valid when MODE=restore")
		}
		if _, err := parseBackupTimestamp(cfg.restoreTimestamp); err != nil {
			return cfg, fmt.Errorf("invalid RESTORE_TIMESTAMP: %w", err)
		}
	}

	if err := validateEncryptionConfig(cfg); err != nil {
		return cfg, err
	}

	level, err := parseLogLevel(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if err != nil {
		return cfg, err
	}
	cfg.logLevel = level

	mode := strings.ToLower(strings.TrimSpace(os.Getenv("S3_ADDRESSING_MODE")))
	if mode == "" {
		mode = string(addressingPath)
	}
	if mode != string(addressingPath) && mode != string(addressingVirtual) && mode != string(addressingAuto) {
		return cfg, fmt.Errorf("invalid S3_ADDRESSING_MODE %q (allowed: auto,path,virtual)", mode)
	}
	cfg.s3AddressingMode = addressingMode(mode)

	keepDays := strings.TrimSpace(os.Getenv("BACKUP_KEEP_DAYS"))
	if keepDays != "" {
		value, parseErr := strconv.Atoi(keepDays)
		if parseErr != nil || value < 0 || value > maxBackupKeepDays {
			return cfg, fmt.Errorf("invalid BACKUP_KEEP_DAYS value %q (allowed: 0-%d)", keepDays, maxBackupKeepDays)
		}
		cfg.backupKeepDays = value
	}

	return cfg, nil
}

func validatePgDumpExtraOpts(opts []string) error {
	for _, opt := range opts {
		longOutputOption := opt == "--file" || strings.HasPrefix(opt, "--file=")
		longFormatOption := opt == "--format" || strings.HasPrefix(opt, "--format=")
		shortOutputOption := strings.HasPrefix(opt, "-f") && !strings.HasPrefix(opt, "--")
		shortFormatOption := strings.HasPrefix(opt, "-F") && !strings.HasPrefix(opt, "--")
		if longOutputOption || longFormatOption || shortOutputOption || shortFormatOption {
			return fmt.Errorf("PGDUMP_EXTRA_OPTS cannot contain %q because backups require custom format on stdout", opt)
		}
	}
	return nil
}

func validateEncryptionConfig(cfg config) error {
	switch cfg.mode {
	case "backup":
		if cfg.ageIdentity != "" {
			return errors.New("AGE_IDENTITY is only valid when MODE=restore")
		}
		if cfg.agePublicKey != "" && cfg.passphrase != "" {
			return errors.New("AGE_PUBLIC_KEY and PASSPHRASE are mutually exclusive; set only one")
		}
		if cfg.agePublicKey != "" {
			if _, err := age.ParseX25519Recipient(cfg.agePublicKey); err != nil {
				return fmt.Errorf("invalid AGE_PUBLIC_KEY: %w", err)
			}
		}
	case "restore":
		if cfg.ageIdentity != "" && cfg.passphrase != "" {
			return errors.New("AGE_IDENTITY and PASSPHRASE are mutually exclusive when restoring")
		}
		identity := cfg.ageIdentity
		if identity == "" && cfg.agePublicKey != "" {
			// Backwards compatibility: older versions documented the private identity
			// in PASSPHRASE together with AGE_PUBLIC_KEY.
			identity = cfg.passphrase
		}
		if cfg.agePublicKey != "" && identity == "" {
			return errors.New("AGE_IDENTITY is required to restore an AGE_PUBLIC_KEY backup")
		}
		if identity != "" {
			identities, err := age.ParseIdentities(strings.NewReader(identity))
			if err != nil {
				return fmt.Errorf("invalid AGE_IDENTITY: %w", err)
			}
			if len(identities) == 0 {
				return errors.New("AGE_IDENTITY contains no identities")
			}
		}
	}

	return nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	if raw == "" {
		return slog.LevelInfo, nil
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(raw))); err != nil {
		return 0, fmt.Errorf("invalid LOG_LEVEL %q (allowed: debug,info,warn,error)", raw)
	}

	return level, nil
}

func parseInterval(schedule string) (time.Duration, error) {
	s := strings.TrimSpace(schedule)
	if s == "" {
		return 0, errors.New("empty schedule")
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid interval %q (use a Go duration such as 12h): %w", schedule, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("schedule duration must be > 0: %q", schedule)
	}
	return d, nil
}

// splitArgs parses shell-like quoting without invoking a shell. It supports
// whitespace separation, single and double quotes, backslash escapes, and empty
// quoted arguments.
func splitArgs(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}

	var args []string
	var current strings.Builder
	var quote rune
	escaped := false
	started := false

	flush := func() {
		args = append(args, current.String())
		current.Reset()
		started = false
	}

	for _, r := range raw {
		if escaped {
			current.WriteRune(r)
			escaped = false
			started = true
			continue
		}

		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
				started = true
				continue
			}
			if quote == r {
				quote = 0
				continue
			}
		}
		if quote == 0 && (r == ' ' || r == '\t' || r == '\r' || r == '\n') {
			if started {
				flush()
			}
			continue
		}

		current.WriteRune(r)
		started = true
	}

	if escaped {
		return nil, errors.New("trailing backslash")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if started {
		flush()
	}

	return args, nil
}
