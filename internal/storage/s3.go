package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/Xm798/placard/internal/config"
)

// s3Client is the S3-compatible backend: AWS S3, Cloudflare R2 and MinIO all
// speak this API. The bucket stays private — nothing here ever presigns a URL,
// because objects are served exclusively through the same-origin render proxy
// (see this package's doc comment).
type s3Client struct {
	api    *s3.Client
	bucket string
}

// NewS3Client builds the S3 backend. An empty Endpoint means AWS S3, where the
// SDK derives the endpoint from the region; R2 and MinIO set it explicitly.
// PathStyle addresses buckets as a path segment, which MinIO requires.
func NewS3Client(cfg config.S3Config) (Client, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("storage s3: bucket is required")
	}
	opts := s3.Options{
		Region:       cfg.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		UsePathStyle: cfg.PathStyle,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}
	return &s3Client{api: s3.New(opts), bucket: cfg.Bucket}, nil
}

func (s *s3Client) PutObject(ctx context.Context, key string, data io.Reader, contentType string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        data,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("storage s3 put object %q: %w", key, err)
	}
	return nil
}

// GetObject streams the object body; the caller must Close it. A missing key
// is reported as ErrNotFound so the handler renders a clean miss.
func (s *s3Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	out, err := s.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("storage s3 get object %q: %w", key, ErrNotFound)
		}
		return nil, fmt.Errorf("storage s3 get object %q: %w", key, err)
	}
	return out.Body, nil
}

// DeleteObject removes key. Idempotent: S3 itself answers 204 for an absent
// key, and the not-found mapping covers backends that answer 404 instead.
func (s *s3Client) DeleteObject(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := s.api.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isS3NotFound(err) {
		return fmt.Errorf("storage s3 delete object %q: %w", key, err)
	}
	return nil
}

// isS3NotFound reports whether err is the service's "object is not there"
// answer. The typed NoSuchKey covers GetObject on AWS and MinIO; the error-code
// fallback catches the bare "NotFound" that HEAD-shaped responses carry, which
// has no response body for the SDK to deserialize into a typed error.
func isS3NotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound")
}
