package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

// doBackup streams pg_dump output, optionally through age encryption, directly
// to S3 — no temporary files are created.
func doBackup(ctx context.Context, cfg config, storage *storageClient) error {
	slog.Info("creating database backup", "database", cfg.postgresDatabase)

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05")
	objectKey := path.Join(normalizedS3Prefix(cfg), fmt.Sprintf("%s_%s.dump", cfg.postgresDatabase, timestamp))
	if cfg.passphrase != "" || cfg.agePublicKey != "" {
		objectKey += ".age"
	}

	slog.Info("uploading backup", "bucket", cfg.s3Bucket, "key", objectKey)
	if err := streamBackup(ctx, cfg, storage, objectKey); err != nil {
		return err
	}

	if cfg.backupKeepDays > 0 {
		if err := pruneOldBackups(ctx, cfg, storage); err != nil {
			return err
		}
	}

	slog.Info("backup complete")
	return nil
}

// streamBackup pipes pg_dump → [age encrypt] → S3 upload without touching disk.
func streamBackup(ctx context.Context, cfg config, storage *storageClient, objectKey string) error {
	dumpPr, dumpPw := io.Pipe()

	cmd := buildPgDumpCmd(cfg)
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

	// Drain/close the source pipe so the goroutines above can unblock and exit.
	_, _ = io.Copy(io.Discard, uploadSrc)

	// Collect errors, preferring the most meaningful one.
	dumpErr := <-dumpErrCh
	if dumpErr != nil {
		dumpErr = fmt.Errorf("pg_dump failed: %w", dumpErr)
	}

	var encErr error
	if encErrCh != nil {
		encErr = <-encErrCh
	}

	if uploadErr != nil {
		return uploadErr
	}
	if dumpErr != nil {
		return dumpErr
	}
	return encErr
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
			_ = agePw.CloseWithError(fmt.Errorf("age encrypt init: %w", err))
			errCh <- err
			return
		}

		if _, err := io.Copy(enc, r); err != nil {
			_ = agePw.CloseWithError(err)
			errCh <- err
			return
		}

		if err := enc.Close(); err != nil {
			_ = agePw.CloseWithError(fmt.Errorf("age encrypt finalize: %w", err))
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

// buildAgeIdentity returns the scrypt identity used for decryption.
// X25519 decryption requires the private key, which is given as the passphrase
// (age identity file format) in that mode.
func buildAgeIdentity(cfg config) (age.Identity, error) {
	if cfg.agePublicKey != "" {
		// PASSPHRASE holds the private key (age identity) when public-key mode was used.
		identities, err := age.ParseIdentities(strings.NewReader(cfg.passphrase))
		if err != nil {
			return nil, fmt.Errorf("invalid age identity in PASSPHRASE: %w", err)
		}
		if len(identities) == 0 {
			return nil, errors.New("no age identity found in PASSPHRASE")
		}
		return identities[0], nil
	}

	id, err := age.NewScryptIdentity(cfg.passphrase)
	if err != nil {
		return nil, fmt.Errorf("age scrypt identity: %w", err)
	}
	return id, nil
}

// doRestore downloads a backup from S3 (decrypting if needed) to a temporary
// file, then calls pg_restore. pg_restore requires a seekable file for the
// custom format, so we cannot fully stream the restore path.
func doRestore(ctx context.Context, cfg config, storage *storageClient, timestamp string) error {
	fileSuffix := ".dump"
	if cfg.passphrase != "" || cfg.agePublicKey != "" {
		fileSuffix = ".dump.age"
	}

	key, err := resolveBackupKey(ctx, cfg, storage, strings.TrimSpace(timestamp), fileSuffix)
	if err != nil {
		return err
	}

	// Download to a temp file (possibly encrypted).
	downloadTmp, err := os.CreateTemp("", "postgres-s3-restore-*.dump"+suffixOf(fileSuffix))
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	downloadTmpName := downloadTmp.Name()
	_ = downloadTmp.Close()
	defer func() { _ = os.Remove(downloadTmpName) }()

	slog.Info("fetching backup", "bucket", cfg.s3Bucket, "key", key)
	if err := storage.downloadFile(ctx, cfg.s3Bucket, key, downloadTmpName); err != nil {
		return err
	}

	// Decrypt if needed, streaming into a second temp file.
	restoreFile := downloadTmpName
	if cfg.passphrase != "" || cfg.agePublicKey != "" {
		decryptedTmp, err := os.CreateTemp("", "postgres-s3-restore-*.dump")
		if err != nil {
			return fmt.Errorf("create temp file for decryption: %w", err)
		}
		decryptedTmpName := decryptedTmp.Name()
		_ = decryptedTmp.Close()
		defer func() { _ = os.Remove(decryptedTmpName) }()

		slog.Info("decrypting backup")
		if err := decryptFile(downloadTmpName, decryptedTmpName, cfg); err != nil {
			return err
		}
		restoreFile = decryptedTmpName
	}

	slog.Info("restoring from backup")
	if err := runPgRestore(cfg, restoreFile); err != nil {
		return err
	}

	slog.Info("restore complete")
	return nil
}

// suffixOf returns the file-type portion after ".dump" for use in temp file names.
func suffixOf(fileSuffix string) string {
	if fileSuffix == ".dump.age" {
		return ".age"
	}
	return ""
}

func decryptFile(inputFile, outputFile string, cfg config) error {
	in, err := os.Open(inputFile)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	identity, err := buildAgeIdentity(cfg)
	if err != nil {
		return err
	}

	dec, err := age.Decrypt(in, identity)
	if err != nil {
		return fmt.Errorf("age decrypt: %w", err)
	}

	if _, err := io.Copy(out, dec); err != nil {
		return fmt.Errorf("age decrypt copy: %w", err)
	}
	return nil
}

func resolveBackupKey(ctx context.Context, cfg config, storage *storageClient, timestamp, fileSuffix string) (string, error) {
	if timestamp != "" {
		return path.Join(normalizedS3Prefix(cfg), fmt.Sprintf("%s_%s%s", cfg.postgresDatabase, timestamp, fileSuffix)), nil
	}

	slog.Info("finding latest backup")
	objects, err := storage.listObjects(ctx, cfg.s3Bucket, backupObjectPrefix(cfg))
	if err != nil {
		return "", err
	}

	var candidates []backupObject
	for _, obj := range objects {
		if strings.HasSuffix(obj.key, fileSuffix) {
			candidates = append(candidates, obj)
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("no backup found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].lastModified.Before(candidates[j].lastModified)
	})

	return candidates[len(candidates)-1].key, nil
}

// doList prints all available backup timestamps for the configured database,
// one per line, sorted oldest to newest.
func doList(ctx context.Context, cfg config, storage *storageClient) error {
	objects, err := storage.listObjects(ctx, cfg.s3Bucket, backupObjectPrefix(cfg))
	if err != nil {
		return err
	}

	if len(objects) == 0 {
		slog.Info("no backups found")
		return nil
	}

	sort.Slice(objects, func(i, j int) bool {
		return objects[i].lastModified.Before(objects[j].lastModified)
	})

	dbPrefix := cfg.postgresDatabase + "_"
	for _, obj := range objects {
		base := path.Base(obj.key)
		if !strings.HasPrefix(base, dbPrefix) {
			continue
		}
		ts := strings.TrimPrefix(base, dbPrefix)
		ts = strings.TrimSuffix(ts, ".dump.age")
		ts = strings.TrimSuffix(ts, ".dump")
		fmt.Println(ts)
	}
	return nil
}

func pruneOldBackups(ctx context.Context, cfg config, storage *storageClient) error {
	cutoff := time.Now().UTC().Add(-time.Duration(cfg.backupKeepDays) * 24 * time.Hour)
	slog.Info("removing old backups", "cutoff", cutoff.Format(time.RFC3339))

	listPrefix := normalizedS3Prefix(cfg)
	if listPrefix != "" {
		listPrefix += "/"
	}

	objects, err := storage.listObjects(ctx, cfg.s3Bucket, listPrefix)
	if err != nil {
		return err
	}

	for _, obj := range objects {
		if obj.lastModified.IsZero() {
			continue
		}
		if obj.lastModified.Before(cutoff) {
			if err := storage.deleteObject(ctx, cfg.s3Bucket, obj.key); err != nil {
				return err
			}
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

func normalizedS3Prefix(cfg config) string {
	return strings.Trim(cfg.s3Prefix, "/")
}

func buildPgDumpCmd(cfg config) *exec.Cmd {
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

	cmd := exec.Command("pg_dump", args...)
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

func runPgRestore(cfg config, inputFile string) error {
	args := []string{
		"-h", cfg.postgresHost,
		"-p", cfg.postgresPort,
		"-U", cfg.postgresUser,
		"-d", cfg.postgresDatabase,
		"--clean",
		"--if-exists",
		inputFile,
	}

	cmd := exec.Command("pg_restore", args...)
	env := os.Environ()
	if cfg.postgresPassword != "" {
		env = append(env, "PGPASSWORD="+cfg.postgresPassword)
	}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore failed: %w", err)
	}
	return nil
}
