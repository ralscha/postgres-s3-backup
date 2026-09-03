package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

// doBackup streams pg_dump output, optionally through age encryption, directly
// to S3; no temporary files are created.
func doBackup(ctx context.Context, cfg config, storage objectStorage) error {
	slog.Info("creating database backup", "database", cfg.postgresDatabase)

	timestamp := time.Now().UTC().Format(backupFilenameTimestampLayout)
	objectKey := backupObjectKey(cfg, timestamp, backupUsesEncryption(cfg))

	slog.Info("uploading backup", "bucket", cfg.s3Bucket, "key", objectKey)
	if err := streamBackup(ctx, cfg, storage, objectKey); err != nil {
		return err
	}

	if cfg.backupKeepDays > 0 {
		if err := pruneOldBackups(ctx, cfg, storage); err != nil {
			return fmt.Errorf("backup uploaded but retention cleanup failed: %w", err)
		}
	}

	slog.Info("backup complete")
	return nil
}

// streamBackup pipes pg_dump through optional age encryption to S3 without touching disk.
func streamBackup(ctx context.Context, cfg config, storage objectStorage, objectKey string) error {
	dumpPr, dumpPw := io.Pipe()

	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()

	cmd := buildPgDumpCmd(processCtx, cfg)
	cmd.Stdout = dumpPw
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = dumpPr.CloseWithError(err)
		_ = dumpPw.Close()
		return fmt.Errorf("pg_dump start failed: %w", err)
	}

	dumpErrCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			_ = dumpPw.CloseWithError(fmt.Errorf("pg_dump failed: %w", err))
		} else {
			_ = dumpPw.Close()
		}
		dumpErrCh <- err
	}()

	uploadSrc, encErrCh := maybeEncrypt(dumpPr, cfg)

	uploadErr := storage.uploadStream(ctx, cfg.s3Bucket, objectKey, uploadSrc)
	if uploadErr != nil {
		// Stop pg_dump promptly when S3 rejects or aborts the upload. Closing the
		// reader also unblocks the optional encryption goroutine.
		cancelProcess()
	}
	_ = uploadSrc.Close()

	// Collect all pipeline errors so failures in PostgreSQL, encryption, and S3
	// remain diagnosable from a single run.
	dumpErr := <-dumpErrCh
	if dumpErr != nil {
		dumpErr = fmt.Errorf("pg_dump failed: %w", dumpErr)
	}

	var encErr error
	if encErrCh != nil {
		encErr = <-encErrCh
	}

	return errors.Join(uploadErr, dumpErr, encErr)
}

// maybeEncrypt wraps r in an age encryption pipe when encryption is configured.
// It returns the reader to be used as the upload source, and (when encryption is
// used) a channel that delivers any encryption error after the stream is consumed.
func maybeEncrypt(r io.ReadCloser, cfg config) (io.ReadCloser, chan error) {
	if cfg.passphrase == "" && cfg.agePublicKey == "" {
		return r, nil
	}

	agePr, agePw := io.Pipe()
	errCh := make(chan error, 1)

	go func() {
		defer func() { _ = r.Close() }()

		recipient, err := buildAgeRecipient(cfg)
		if err != nil {
			_ = agePw.CloseWithError(err)
			errCh <- err
			return
		}

		enc, err := age.Encrypt(agePw, recipient)
		if err != nil {
			err = fmt.Errorf("age encrypt init: %w", err)
			_ = agePw.CloseWithError(err)
			errCh <- err
			return
		}

		if _, err := io.Copy(enc, r); err != nil {
			err = fmt.Errorf("age encrypt data: %w", err)
			_ = agePw.CloseWithError(err)
			errCh <- err
			return
		}

		if err := enc.Close(); err != nil {
			err = fmt.Errorf("age encrypt finalize: %w", err)
			_ = agePw.CloseWithError(err)
			errCh <- err
			return
		}

		_ = agePw.Close()
		errCh <- nil
	}()

	return agePr, errCh
}

// buildAgeRecipient returns either an X25519 public-key recipient or a
// scrypt (passphrase) recipient depending on the configuration.
func buildAgeRecipient(cfg config) (age.Recipient, error) {
	if cfg.agePublicKey != "" {
		r, err := age.ParseX25519Recipient(cfg.agePublicKey)
		if err != nil {
			return nil, fmt.Errorf("invalid AGE_PUBLIC_KEY: %w", err)
		}
		return r, nil
	}

	r, err := age.NewScryptRecipient(cfg.passphrase)
	if err != nil {
		return nil, fmt.Errorf("age scrypt recipient: %w", err)
	}
	r.SetWorkFactor(cfg.ageWorkFactor)
	return r, nil
}

// buildAgeIdentities returns the configured identities used for decryption.
func buildAgeIdentities(cfg config) ([]age.Identity, error) {
	identityText := cfg.ageIdentity
	if identityText == "" && cfg.agePublicKey != "" {
		// Backwards compatibility with the original configuration, which stored
		// the private identity in PASSPHRASE when AGE_PUBLIC_KEY was also set.
		identityText = cfg.passphrase
	}
	if identityText != "" {
		identities, err := age.ParseIdentities(strings.NewReader(identityText))
		if err != nil {
			return nil, fmt.Errorf("invalid age identity: %w", err)
		}
		if len(identities) == 0 {
			return nil, errors.New("no age identity found")
		}
		return identities, nil
	}

	id, err := age.NewScryptIdentity(cfg.passphrase)
	if err != nil {
		return nil, fmt.Errorf("age scrypt identity: %w", err)
	}
	return []age.Identity{id}, nil
}

// doRestore downloads a backup from S3 (decrypting if needed) to a temporary
// file, then calls pg_restore. pg_restore requires a seekable file for the
// custom format, so we cannot fully stream the restore path.
func doRestore(ctx context.Context, cfg config, storage objectStorage, timestamp string) error {
	encrypted := restoreUsesEncryption(cfg)
	key, err := resolveBackupKey(ctx, cfg, storage, strings.TrimSpace(timestamp), encrypted)
	if err != nil {
		return err
	}

	// Download to a temp file (possibly encrypted).
	tempPattern := "postgres-s3-restore-*.dump"
	if encrypted {
		tempPattern += ".age"
	}
	downloadTmp, err := os.CreateTemp("", tempPattern)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	downloadTmpName := downloadTmp.Name()
	defer func() { _ = os.Remove(downloadTmpName) }()
	if err := downloadTmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	slog.Info("fetching backup", "bucket", cfg.s3Bucket, "key", key)
	if err := storage.downloadFile(ctx, cfg.s3Bucket, key, downloadTmpName); err != nil {
		return err
	}

	// Decrypt if needed, streaming into a second temp file.
	restoreFile := downloadTmpName
	if encrypted {
		decryptedTmp, err := os.CreateTemp("", "postgres-s3-restore-*.dump")
		if err != nil {
			return fmt.Errorf("create temp file for decryption: %w", err)
		}
		decryptedTmpName := decryptedTmp.Name()
		defer func() { _ = os.Remove(decryptedTmpName) }()
		if err := decryptedTmp.Close(); err != nil {
			return fmt.Errorf("close temp file for decryption: %w", err)
		}

		slog.Info("decrypting backup")
		if err := decryptFile(downloadTmpName, decryptedTmpName, cfg); err != nil {
			return err
		}
		restoreFile = decryptedTmpName
	}

	slog.Info("restoring from backup")
	if err := runPgRestore(ctx, cfg, restoreFile); err != nil {
		return err
	}

	slog.Info("restore complete")
	return nil
}

func decryptFile(inputFile, outputFile string, cfg config) (returnErr error) {
	// inputFile is a private temporary file created by doRestore.
	//nolint:gosec // The path is generated internally rather than supplied by a user.
	in, err := os.Open(inputFile)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	// outputFile is a private temporary file created by doRestore.
	//nolint:gosec // The path is generated internally rather than supplied by a user.
	out, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer func() {
		if err := out.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close decrypted file: %w", err)
		}
	}()

	identities, err := buildAgeIdentities(cfg)
	if err != nil {
		return err
	}

	dec, err := age.Decrypt(in, identities...)
	if err != nil {
		return fmt.Errorf("age decrypt: %w", err)
	}

	if _, err := io.Copy(out, dec); err != nil {
		return fmt.Errorf("age decrypt copy: %w", err)
	}
	return nil
}

func resolveBackupKey(ctx context.Context, cfg config, storage objectStorage, timestamp string, encrypted bool) (string, error) {
	if timestamp != "" {
		if _, err := parseBackupTimestamp(timestamp); err != nil {
			return "", err
		}
		return backupObjectKey(cfg, timestamp, encrypted), nil
	}

	slog.Info("finding latest backup")
	objects, err := storage.listObjects(ctx, cfg.s3Bucket, backupObjectPrefix(cfg))
	if err != nil {
		return "", err
	}

	var candidates []backupInfo
	for _, obj := range objects {
		if backup, ok := parseBackupObject(cfg, obj.key); ok && backup.encrypted == encrypted {
			candidates = append(candidates, backup)
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("no backup found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].createdAt.Equal(candidates[j].createdAt) {
			return candidates[i].key < candidates[j].key
		}
		return candidates[i].createdAt.Before(candidates[j].createdAt)
	})

	return candidates[len(candidates)-1].key, nil
}

// doList prints all available backup timestamps for the configured database,
// one per line, sorted oldest to newest.
func doList(ctx context.Context, cfg config, storage objectStorage) error {
	objects, err := storage.listObjects(ctx, cfg.s3Bucket, backupObjectPrefix(cfg))
	if err != nil {
		return err
	}

	backups := validBackups(cfg, objects)
	if len(backups) == 0 {
		slog.Info("no backups found")
		return nil
	}

	sort.Slice(backups, func(i, j int) bool {
		if backups[i].createdAt.Equal(backups[j].createdAt) {
			return backups[i].key < backups[j].key
		}
		return backups[i].createdAt.Before(backups[j].createdAt)
	})

	for _, backup := range backups {
		fmt.Println(backup.timestamp)
	}
	return nil
}

func pruneOldBackups(ctx context.Context, cfg config, storage objectStorage) error {
	cutoff := time.Now().UTC().Add(-time.Duration(cfg.backupKeepDays) * 24 * time.Hour)
	slog.Info("removing old backups", "cutoff", cutoff.Format(time.RFC3339))

	objects, err := storage.listObjects(ctx, cfg.s3Bucket, backupObjectPrefix(cfg))
	if err != nil {
		return err
	}

	for _, backup := range validBackups(cfg, objects) {
		if backup.createdAt.Before(cutoff) {
			if err := storage.deleteObject(ctx, cfg.s3Bucket, backup.key); err != nil {
				return err
			}
			slog.Debug("removed old backup", "key", backup.key)
		}
	}

	slog.Info("removal complete")
	return nil
}

func backupObjectPrefix(cfg config) string {
	prefix := normalizedS3Prefix(cfg)
	if prefix == "" {
		return cfg.postgresDatabase + "_"
	}
	return prefix + "/" + cfg.postgresDatabase + "_"
}

func backupObjectKey(cfg config, timestamp string, encrypted bool) string {
	key := backupObjectPrefix(cfg) + timestamp + ".dump"
	if encrypted {
		key += ".age"
	}
	return key
}

func backupUsesEncryption(cfg config) bool {
	return cfg.passphrase != "" || cfg.agePublicKey != ""
}

func restoreUsesEncryption(cfg config) bool {
	return cfg.passphrase != "" || cfg.agePublicKey != "" || cfg.ageIdentity != ""
}

func parseBackupTimestamp(value string) (time.Time, error) {
	timestamp, err := time.ParseInLocation(backupTimestampLayout, value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DDTHH:MM:SS with optional fractional seconds: %w", err)
	}
	return timestamp, nil
}

func parseBackupObject(cfg config, key string) (backupInfo, bool) {
	name, found := strings.CutPrefix(key, backupObjectPrefix(cfg))
	if !found || name == "" || strings.ContainsRune(name, '/') {
		return backupInfo{}, false
	}

	encrypted := false
	switch {
	case strings.HasSuffix(name, ".dump.age"):
		encrypted = true
		name = strings.TrimSuffix(name, ".dump.age")
	case strings.HasSuffix(name, ".dump"):
		name = strings.TrimSuffix(name, ".dump")
	default:
		return backupInfo{}, false
	}

	timestamp, err := parseBackupTimestamp(name)
	if err != nil {
		return backupInfo{}, false
	}
	return backupInfo{key: key, timestamp: name, createdAt: timestamp, encrypted: encrypted}, true
}

func validBackups(cfg config, objects []backupObject) []backupInfo {
	backups := make([]backupInfo, 0, len(objects))
	for _, obj := range objects {
		if backup, ok := parseBackupObject(cfg, obj.key); ok {
			backups = append(backups, backup)
		}
	}
	return backups
}

func normalizedS3Prefix(cfg config) string {
	return strings.Trim(strings.TrimSpace(cfg.s3Prefix), "/")
}

func buildPgDumpCmd(ctx context.Context, cfg config) *exec.Cmd {
	args := make([]string, 0, 11+len(cfg.pgDumpExtraOpts))
	args = append(args,
		"--format=custom",
		"--compress", fmt.Sprintf("%d", cfg.pgDumpCompressionLevel),
		"-h", cfg.postgresHost,
		"-p", cfg.postgresPort,
		"-U", cfg.postgresUser,
		"-d", cfg.postgresDatabase,
	)

	if hasPgDumpCompressionOption(cfg.pgDumpExtraOpts) {
		filtered := make([]string, 0, len(args)-2)
		for i := 0; i < len(args); i++ {
			if args[i] == "--compress" && i+1 < len(args) {
				i++
				continue
			}
			filtered = append(filtered, args[i])
		}
		args = filtered
	}
	args = append(args, cfg.pgDumpExtraOpts...)

	//nolint:gosec // The executable is fixed and arguments are passed directly without a shell.
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	env := os.Environ()
	if cfg.postgresPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.postgresPassword)
	}
	cmd.Env = env
	return cmd
}

func hasPgDumpCompressionOption(opts []string) bool {
	for i := range opts {
		opt := opts[i]
		if opt == "--compress" || strings.HasPrefix(opt, "--compress=") {
			return true
		}
		if opt == "-Z" {
			return true
		}
		if strings.HasPrefix(opt, "-Z") && len(opt) > 2 {
			return true
		}
	}
	return false
}

func buildPgRestoreCmd(ctx context.Context, cfg config, inputFile string) *exec.Cmd {
	args := []string{
		"-h", cfg.postgresHost,
		"-p", cfg.postgresPort,
		"-U", cfg.postgresUser,
		"-d", cfg.postgresDatabase,
	}
	if cfg.pgRestoreClean {
		args = append(args, "--clean", "--if-exists")
	}
	args = append(args, cfg.pgRestoreExtraOpts...)
	args = append(args, inputFile)

	//nolint:gosec // The executable is fixed and arguments are passed directly without a shell.
	cmd := exec.CommandContext(ctx, "pg_restore", args...)
	env := os.Environ()
	if cfg.postgresPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.postgresPassword)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func runPgRestore(ctx context.Context, cfg config, inputFile string) error {
	cmd := buildPgRestoreCmd(ctx, cfg, inputFile)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore failed: %w", err)
	}
	return nil
}
