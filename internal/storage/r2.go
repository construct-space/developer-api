// R2 storage — uploads space bundles to Cloudflare R2
// Same pattern as base-core: AWS SDK v1, S3-compatible
package storage

import (
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

type R2 struct {
	client *s3.S3
	bucket string
	cdn    string
}

func NewR2FromEnv() (*R2, error) {
	accountID := firstNonEmpty(os.Getenv("STORAGE_ACCOUNT_ID"), os.Getenv("R2_ACCOUNT_ID"))
	accessKey := firstNonEmpty(os.Getenv("STORAGE_API_KEY"), os.Getenv("R2_ACCESS_KEY_ID"))
	secretKey := firstNonEmpty(os.Getenv("STORAGE_API_SECRET"), os.Getenv("R2_SECRET_ACCESS_KEY"))
	bucket := firstNonEmpty(os.Getenv("STORAGE_BUCKET"), os.Getenv("R2_BUCKET"))
	cdn := firstNonEmpty(os.Getenv("STORAGE_PUBLIC_URL"), os.Getenv("R2_PUBLIC_URL"))
	endpoint := os.Getenv("STORAGE_ENDPOINT")

	if accountID == "" || accessKey == "" || secretKey == "" || bucket == "" {
		return nil, fmt.Errorf("R2 not configured")
	}
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	}

	sess, err := session.NewSession(&aws.Config{
		Credentials:      credentials.NewStaticCredentials(accessKey, secretKey, ""),
		Endpoint:         aws.String(endpoint),
		Region:           aws.String("auto"),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("r2 session: %w", err)
	}

	return &R2{
		client: s3.New(sess),
		bucket: bucket,
		cdn:    cdn,
	}, nil
}

// Upload uploads a local file to R2
func (r *R2) Upload(key string, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = r.client.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String("application/gzip"),
	})
	return err
}

// URL returns the CDN URL for a key
func (r *R2) URL(key string) string {
	if r.cdn != "" {
		return fmt.Sprintf("%s/%s", strings.TrimRight(r.cdn, "/"), key)
	}
	return fmt.Sprintf("https://%s.r2.dev/%s", r.bucket, key)
}

// Exists checks if an object exists
func (r *R2) Exists(key string) bool {
	_, err := r.client.HeadObject(&s3.HeadObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	return err == nil
}

// GetObject returns the object body (caller must close)
func (r *R2) GetObject(key string) (*s3.GetObjectOutput, error) {
	return r.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
}

// BundleKey returns the R2 key for a space bundle
func BundleKey(spaceName, version string) string {
	return fmt.Sprintf("spaces/%s/%s/bundle.tar.gz", spaceName, version)
}

// SourceKey returns the R2 key for a space source
func SourceKey(spaceName, version string) string {
	return fmt.Sprintf("sources/%s/%s/source.tar.gz", spaceName, version)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
