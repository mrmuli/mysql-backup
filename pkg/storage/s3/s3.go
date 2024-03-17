package s3

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	log "github.com/sirupsen/logrus"
)

type S3 struct {
	url url.URL
	// pathStyle option is not really used, but may be required
	// at some point; see https://aws.amazon.com/blogs/aws/amazon-s3-path-deprecation-plan-the-rest-of-the-story/
	pathStyle       bool
	region          string
	endpoint        string
	accessKeyId     string
	secretAccessKey string
}

type Option func(s *S3)

func WithPathStyle() Option {
	return func(s *S3) {
		s.pathStyle = true
	}
}
func WithRegion(region string) Option {
	return func(s *S3) {
		s.region = region
	}
}
func WithEndpoint(endpoint string) Option {
	return func(s *S3) {
		s.endpoint = endpoint
	}
}
func WithAccessKeyId(accessKeyId string) Option {
	return func(s *S3) {
		s.accessKeyId = accessKeyId
	}
}
func WithSecretAccessKey(secretAccessKey string) Option {
	return func(s *S3) {
		s.secretAccessKey = secretAccessKey
	}
}

func New(u url.URL, opts ...Option) *S3 {
	s := &S3{url: u}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *S3) Pull(source, target string) (int64, error) {
	// TODO: need to find way to include cli opts and cli_s3_cp_opts
	// old was:
	// 		aws ${AWS_CLI_OPTS} s3 cp ${AWS_CLI_S3_CP_OPTS} "${DB_RESTORE_TARGET}" $TMPRESTORE

	bucket, path := s.url.Hostname(), path.Join(s.url.Path, source)
	// The session the S3 Downloader will use
	cfg, err := getConfig(s.endpoint)
	if err != nil {
		return 0, fmt.Errorf("failed to load AWS config: %v", err)
	}

	client := s3.NewFromConfig(cfg)

	// Create a downloader with the session and default options
	downloader := manager.NewDownloader(client)

	// Create a file to write the S3 Object contents to.
	f, err := os.Create(target)
	if err != nil {
		return 0, fmt.Errorf("failed to create target restore file %q, %v", target, err)
	}
	defer f.Close()

	// Write the contents of S3 Object to the file
	n, err := downloader.Download(context.TODO(), f, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to download file, %v", err)
	}
	return n, nil
}

func (s *S3) Push(target, source string) (int64, error) {
	// TODO: need to find way to include cli opts and cli_s3_cp_opts
	// old was:
	// 		aws ${AWS_CLI_OPTS} s3 cp ${AWS_CLI_S3_CP_OPTS} "${DB_RESTORE_TARGET}" $TMPRESTORE

	bucket, key := s.url.Hostname(), s.url.Path
	// The session the S3 Downloader will use
	cfg, err := getConfig(s.endpoint)
	if err != nil {
		return 0, fmt.Errorf("failed to load AWS config: %v", err)
	}

	client := s3.NewFromConfig(cfg)
	// Create an uploader with the session and default options
	uploader := manager.NewUploader(client)

	// Create a file to write the S3 Object contents to.
	f, err := os.Open(source)
	if err != nil {
		return 0, fmt.Errorf("failed to read input file %q, %v", source, err)
	}
	defer f.Close()

	// Write the contents of the file to the S3 object
	_, err = uploader.Upload(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(path.Join(key, target)),
		Body:   f,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to upload file, %v", err)
	}
	return 0, nil
}

func (s *S3) Protocol() string {
	return "s3"
}

func (s *S3) URL() string {
	return s.url.String()
}

func (s *S3) ReadDir(dirname string) ([]fs.FileInfo, error) {
	// Get the AWS config
	cfg, err := getConfig(s.endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %v", err)
	}

	// Create a new S3 service client
	svc := s3.NewFromConfig(cfg)

	// Call ListObjectsV2 with your bucket and prefix
	result, err := svc.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{Bucket: aws.String(s.url.Hostname()), Prefix: aws.String(dirname)})
	if err != nil {
		return nil, fmt.Errorf("failed to list objects, %v", err)
	}

	// Convert s3.Object to fs.FileInfo
	var files []fs.FileInfo
	for _, item := range result.Contents {
		files = append(files, &s3FileInfo{
			name:         *item.Key,
			lastModified: *item.LastModified,
			size:         item.Size,
		})
	}

	return files, nil
}

func (s *S3) Remove(target string) error {
	// Get the AWS config
	cfg, err := getConfig(s.endpoint)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %v", err)
	}

	// Create a new S3 service client
	svc := s3.NewFromConfig(cfg)

	// Call DeleteObject with your bucket and the key of the object you want to delete
	_, err = svc.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(s.url.Hostname()),
		Key:    aws.String(target),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object, %v", err)
	}

	return nil
}

func getEndpoint(endpoint string) string {
	// for some reason, the lookup gets flaky when the endpoint is 127.0.0.1
	// so you have to set it to localhost explicitly.
	e := endpoint
	u, err := url.Parse(endpoint)
	if err == nil {
		if u.Hostname() == "127.0.0.1" {
			port := u.Port()
			u.Host = "localhost"
			if port != "" {
				u.Host += ":" + port
			}
			e = u.String()
		}
	}
	return e
}

func getConfig(endpoint string) (aws.Config, error) {
	cleanEndpoint := getEndpoint(endpoint)
	opts := []func(*config.LoadOptions) error{
		config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: cleanEndpoint}, nil
			}),
		),
	}
	if log.IsLevelEnabled(log.TraceLevel) {
		opts = append(opts, config.WithClientLogMode(aws.LogRequestWithBody|aws.LogResponse))
	}
	return config.LoadDefaultConfig(context.TODO(),
		opts...,
	)

}

type s3FileInfo struct {
	name         string
	lastModified time.Time
	size         int64
}

func (s s3FileInfo) Name() string       { return s.name }
func (s s3FileInfo) Size() int64        { return s.size }
func (s s3FileInfo) Mode() os.FileMode  { return 0 } // Not applicable in S3
func (s s3FileInfo) ModTime() time.Time { return s.lastModified }
func (s s3FileInfo) IsDir() bool        { return false } // Not applicable in S3
func (s s3FileInfo) Sys() interface{}   { return nil }   // Not applicable in S3
