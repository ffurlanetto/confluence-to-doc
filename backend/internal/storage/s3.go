package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3Options configures the S3 blob store.
type S3Options struct {
	Bucket          string
	Region          string
	Endpoint        string // empty for AWS S3
	AccessKeyID     string // empty to use the AWS default credential chain
	SecretAccessKey string
	Prefix          string
	UsePathStyle    bool
}

// S3 stores documents in an S3 (or S3-compatible) bucket, so API and worker
// instances can run on different machines without a shared volume.
type S3 struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewS3 builds the client and checks that the bucket is reachable, so a
// misconfiguration fails at startup rather than on the first export.
func NewS3(ctx context.Context, o S3Options) (*S3, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(o.Region)}
	if o.AccessKeyID != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(o.AccessKeyID, o.SecretAccessKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("storage: loading AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(so *s3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
		so.UsePathStyle = o.UsePathStyle
	})
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(o.Bucket)}); err != nil {
		return nil, fmt.Errorf("storage: bucket %q is not accessible: %w", o.Bucket, err)
	}
	return &S3{client: client, bucket: o.Bucket, prefix: o.Prefix}, nil
}

func (s *S3) objectKey(key string) (string, error) {
	if !keyPattern.MatchString(key) {
		return "", ErrInvalidKey
	}
	return path.Join(s.prefix, key), nil
}

// Put spools r to a temporary file, then uploads it with a single PutObject.
// Spooling gives the SDK a seekable body of known length (required for
// request signing) and never leaves a partial object behind on failure.
// Generated documents are far below the 5 GiB single-request limit.
func (s *S3) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	k, err := s.objectKey(key)
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp("", "c2d-upload-*")
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	n, err := io.Copy(tmp, r)
	if err != nil {
		return 0, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	in := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k), Body: tmp, ContentLength: aws.Int64(n)}
	if ct := mime.TypeByExtension(path.Ext(key)); ct != "" {
		in.ContentType = aws.String(ct)
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return 0, fmt.Errorf("storage: uploading %s: %w", k, err)
	}
	return n, nil
}

// Open returns a seekable reader; data is fetched lazily with ranged GETs so
// HTTP Range requests on downloads map to partial S3 reads.
func (s *S3) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	k, err := s.objectKey(key)
	if err != nil {
		return nil, err
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k)})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("storage: %s: %w", k, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("storage: reading %s: %w", k, err)
	}
	return &s3Object{ctx: ctx, s: s, key: k, size: aws.ToInt64(head.ContentLength)}, nil
}

// Delete is idempotent: S3 does not report an error for a missing object.
func (s *S3) Delete(ctx context.Context, key string) error {
	k, err := s.objectKey(key)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k)}); err != nil {
		return fmt.Errorf("storage: deleting %s: %w", k, err)
	}
	return nil
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}

// s3Object implements io.ReadSeekCloser over an S3 object.
type s3Object struct {
	ctx  context.Context
	s    *S3
	key  string
	size int64
	off  int64
	body io.ReadCloser
}

func (o *s3Object) Read(p []byte) (int, error) {
	if o.off >= o.size {
		return 0, io.EOF
	}
	if o.body == nil {
		out, err := o.s.client.GetObject(o.ctx, &s3.GetObjectInput{
			Bucket: aws.String(o.s.bucket),
			Key:    aws.String(o.key),
			Range:  aws.String(fmt.Sprintf("bytes=%d-", o.off)),
		})
		if err != nil {
			return 0, fmt.Errorf("storage: reading %s: %w", o.key, err)
		}
		o.body = out.Body
	}
	n, err := o.body.Read(p)
	o.off += int64(n)
	if errors.Is(err, io.EOF) && o.off < o.size {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (o *s3Object) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = o.off + offset
	case io.SeekEnd:
		abs = o.size + offset
	default:
		return 0, errors.New("storage: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("storage: negative position")
	}
	if abs != o.off && o.body != nil {
		_ = o.body.Close() // the next Read issues a new ranged GET
		o.body = nil
	}
	o.off = abs
	return abs, nil
}

func (o *s3Object) Close() error {
	if o.body != nil {
		err := o.body.Close()
		o.body = nil
		return err
	}
	return nil
}
