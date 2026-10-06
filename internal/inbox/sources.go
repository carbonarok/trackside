// Package inbox imports reference and timetable files that arrive in a
// folder or cloud bucket, which is how the Rail Data Marketplace delivers
// file products such as SCHEDULE, CORPUS and SMART.
package inbox

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Object is a file waiting in an inbox.
type Object struct {
	Name     string
	Size     int64
	Modified time.Time
}

// Source lists and opens files.
type Source interface {
	List(ctx context.Context) ([]Object, error)
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	String() string
}

// Dir is a local folder, for SFTP deliveries or manual downloads.
type Dir struct {
	Path string
}

func (d Dir) String() string { return d.Path }

// List returns the files directly in the folder.
func (d Dir) List(ctx context.Context) ([]Object, error) {
	entries, err := os.ReadDir(d.Path)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Object{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().UTC().Truncate(time.Second)})
	}
	return out, nil
}

// Open opens a file in the folder.
func (d Dir) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(d.Path, filepath.Base(name)))
}

// Bucket is a Google Cloud Storage or Amazon S3 bucket, read through the S3
// API. Google Cloud Storage needs HMAC keys (Cloud Storage > Settings >
// Interoperability).
type Bucket struct {
	client *minio.Client
	bucket string
	prefix string
	url    string
}

// NewBucket parses gs://bucket/prefix or s3://bucket/prefix. endpoint may
// be empty to use the provider's default.
func NewBucket(rawURL, endpoint, accessKey, secretKey string) (*Bucket, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("inbox bucket %q: want gs://bucket/prefix or s3://bucket/prefix", rawURL)
	}
	if endpoint == "" {
		switch u.Scheme {
		case "gs":
			endpoint = "storage.googleapis.com"
		case "s3":
			endpoint = "s3.amazonaws.com"
		default:
			return nil, fmt.Errorf("inbox bucket %q: unsupported scheme %q", rawURL, u.Scheme)
		}
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: true,
	})
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimPrefix(u.Path, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Bucket{client: client, bucket: u.Host, prefix: prefix, url: rawURL}, nil
}

func (b *Bucket) String() string { return b.url }

// List returns the objects under the prefix, including in sub-folders,
// since the marketplace may deliver each product into its own folder.
func (b *Bucket) List(ctx context.Context) ([]Object, error) {
	var out []Object
	for o := range b.client.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{Prefix: b.prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		if strings.HasSuffix(o.Key, "/") {
			continue
		}
		out = append(out, Object{Name: o.Key, Size: o.Size, Modified: o.LastModified.UTC().Truncate(time.Second)})
	}
	return out, nil
}

// Open streams an object.
func (b *Bucket) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	return b.client.GetObject(ctx, b.bucket, name, minio.GetObjectOptions{})
}
