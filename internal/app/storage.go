package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type storageClient struct {
	client   *s3.Client
	transfer *transfermanager.Client
}

func newStorageClient(ctx context.Context, cfg config) (*storageClient, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.s3Region),
	}

	if cfg.s3AccessKeyID != "" || cfg.s3SecretAccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.s3AccessKeyID,
			cfg.s3SecretAccessKey,
			"",
		)))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}

	usePathStyle := shouldUsePathStyle(cfg)

	clientOpts := func(o *s3.Options) {
		o.UsePathStyle = usePathStyle
		if cfg.s3Endpoint != "" {
			endpoint := strings.TrimSpace(cfg.s3Endpoint)
			u, parseErr := url.Parse(endpoint)
			if parseErr == nil && u.Scheme == "" {
				endpoint = "https://" + endpoint
			}
			o.BaseEndpoint = aws.String(endpoint)
		}
	}

	client := s3.NewFromConfig(awsCfg, clientOpts)

	return &storageClient{
		client:   client,
		transfer: transfermanager.New(client),
	}, nil
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
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	_, err = s.transfer.DownloadObject(ctx, &transfermanager.DownloadObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		WriterAt: f,
	})
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
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
			modified := time.Time{}
			if obj.LastModified != nil {
				modified = *obj.LastModified
			}
			items = append(items, backupObject{
				key:          *obj.Key,
				lastModified: modified,
			})
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
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil
		}
		return fmt.Errorf("delete object %q failed: %w", key, err)
	}
	return nil
}
