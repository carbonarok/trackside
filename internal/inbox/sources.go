// Package inbox imports reference and timetable files that arrive in a
// folder or cloud bucket, which is how the Rail Data Marketplace delivers
// file products such as SCHEDULE, CORPUS and SMART.
package inbox

import (
	"context"
	"fmt"
	"io"
	"io/fs"
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

// List returns the files in the folder and its sub-folders (the marketplace
// delivers some products, such as Darwin's, into their own folder). Hidden
// files and folders are skipped. Names are relative to the folder.
func (d Dir) List(ctx context.Context) ([]Object, error) {
	var out []Object
	err := filepath.WalkDir(d.Path, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(e.Name(), ".") && p != d.Path {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() || !e.Type().IsRegular() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(d.Path, p)
		if err != nil {
			return nil
		}
		out = append(out, Object{Name: filepath.ToSlash(rel), Size: info.Size(),
			Modified: info.ModTime().UTC().Truncate(time.Second)})
		return nil
	})
	return out, err
}

// Open opens a file in the folder. Names that would escape it are refused.
func (d Dir) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	p := filepath.Join(d.Path, filepath.FromSlash(name))
	if rel, err := filepath.Rel(d.Path, p); err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("inbox: %q is outside %s", name, d.Path)
	}
	return os.Open(p)
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
