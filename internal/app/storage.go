package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type storageClient struct {
	client   *s3.Client
	transfer *transfermanager.Client
}

type objectStorage interface {
	uploadStream(context.Context, string, string, io.Reader) error
	downloadFile(context.Context, string, string, string) error
	listObjects(context.Context, string, string) ([]backupObject, error)
	deleteObject(context.Context, string, string) error
}

func newStorageClient(ctx context.Context, cfg config) (*storageClient, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.s3Region),
	}

	if cfg.s3AccessKeyID != "" || cfg.s3SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.s3AccessKeyID,
			cfg.s3SecretAccessKey,
			cfg.s3SessionToken,
		)))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}

	clientOpts := func(o *s3.Options) {
		o.UsePathStyle = shouldUsePathStyle(cfg)
		if cfg.s3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.s3Endpoint)
		}
	}

	client := s3.NewFromConfig(awsCfg, clientOpts)

	return &storageClient{
		client:   client,
		transfer: transfermanager.New(client),
	}, nil
}

func normalizeS3Endpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return "", nil
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid S3_ENDPOINT: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("invalid S3_ENDPOINT (expected an HTTP or HTTPS URL)")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid S3_ENDPOINT (query strings and fragments are not supported)")
	}
	if u.User != nil {
		return "", errors.New("invalid S3_ENDPOINT (embedded credentials are not supported)")
	}

	return strings.TrimRight(u.String(), "/"), nil
}

func shouldUsePathStyle(cfg config) bool {
	switch cfg.s3AddressingMode {
	case addressingPath:
		return true
	case addressingVirtual:
		return false
	case addressingAuto:
		if cfg.s3Endpoint != "" {
			return true
		}
		return false
	default:
		return false
	}
}

func (s *storageClient) uploadStream(ctx context.Context, bucket, key string, body io.Reader) error {
	_, err := s.transfer.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	return nil
}

func (s *storageClient) downloadFile(ctx context.Context, bucket, key, filePath string) error {
	//nolint:gosec // The caller provides a private temporary path created by doRestore.
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	_, downloadErr := s.transfer.DownloadObject(ctx, &transfermanager.DownloadObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		WriterAt: f,
	})
	closeErr := f.Close()
	if downloadErr != nil {
		return fmt.Errorf("download failed: %w", downloadErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close downloaded file: %w", closeErr)
	}
	return nil
}

func (s *storageClient) listObjects(ctx context.Context, bucket, prefix string) ([]backupObject, error) {
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	items := make([]backupObject, 0, 128)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list objects failed: %w", err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			items = append(items, backupObject{key: *obj.Key})
		}
	}

	return items, nil
}

func (s *storageClient) deleteObject(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object %q failed: %w", key, err)
	}
	return nil
}
