package billing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// s3UploadPartSize is the multipart part size used by the transfermanager
// client. 64 MiB balances throughput against memory pressure and keeps part
// counts well under the S3 10,000-part limit for archive bundles up to ~640 GiB.
const s3UploadPartSize = 64 * 1024 * 1024

// S3ObjectStore is an ObjectStore backed by any S3-compatible endpoint
// (AWS S3, minio, Cloudflare R2, GCS S3-interop). Large streaming uploads go
// through the AWS SDK transfermanager client, which switches to multipart
// upload automatically based on size; small synchronous puts use the raw
// *s3.Client to avoid transfermanager overhead.
type S3ObjectStore struct {
	client   *s3.Client
	uploader *transfermanager.Client
	bucket   string
}

// Compile-time check that S3ObjectStore satisfies the ObjectStore interface.
var _ ObjectStore = (*S3ObjectStore)(nil)

// NewS3ObjectStore constructs an S3ObjectStore for the given bucket. The
// supplied *s3.Client must already be configured with credentials, region,
// and (for non-AWS providers) endpoint resolver.
func NewS3ObjectStore(client *s3.Client, bucket string) *S3ObjectStore {
	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = s3UploadPartSize
	})
	return &S3ObjectStore{
		client:   client,
		uploader: uploader,
		bucket:   bucket,
	}
}

// Upload streams r to s3://bucket/key, switching to multipart upload
// automatically for objects larger than the configured threshold. sizeHint
// is forwarded as ContentLength when >= 0; pass -1 if unknown.
func (s *S3ObjectStore) Upload(ctx context.Context, key string, r io.Reader, sizeHint int64) (string, error) {
	input := &transfermanager.UploadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   r,
	}
	if sizeHint >= 0 {
		input.ContentLength = aws.Int64(sizeHint)
	}
	if _, err := s.uploader.UploadObject(ctx, input); err != nil {
		return "", fmt.Errorf("s3: upload %s: %w", key, err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

// GetBytes fetches a small object in full via GetObject, mapping NoSuchKey to
// ErrObjectNotFound for non-retryable manifest-missing handling in restore.
func (s *S3ObjectStore) GetBytes(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("s3: read %s: %w", key, err)
	}
	return data, nil
}

// Download streams an object via GetObject. The caller is responsible for
// closing the returned reader; large encrypted parquet streams are consumed
// chunk-by-chunk by the chunked-frame decoder.
func (s *S3ObjectStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return nil, fmt.Errorf("s3: get %s: %w", key, err)
	}
	return out.Body, nil
}

// PutBytes writes a small, in-memory object (manifest JSON, checksum file)
// via the raw S3 client. Multipart machinery is unnecessary here.
func (s *S3ObjectStore) PutBytes(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("s3: put %s: %w", key, err)
	}
	return nil
}
