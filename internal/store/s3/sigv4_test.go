package s3

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS's documented SigV4 example (S3 "GET Object" with a Range header).
// https://docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html
func TestSignAWSExample(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	sign(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", emptyPayloadSHA, now)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// AWS's documented example for a list request with a query string.
func TestSignAWSListExample(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	sign(req, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", emptyPayloadSHA, now)
	if !strings.HasSuffix(req.Header.Get("Authorization"), "Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7") {
		t.Fatalf("got %s", req.Header.Get("Authorization"))
	}
}

func TestURLs(t *testing.T) {
	cases := []struct {
		cfg  Config
		key  string
		want string
	}{
		{Config{Bucket: "b", Region: "us-east-1"}, "packs/x", "https://b.s3.amazonaws.com/packs/x"},
		{Config{Bucket: "b", Region: "eu-west-2"}, "packs/x", "https://b.s3.eu-west-2.amazonaws.com/packs/x"},
		{Config{Bucket: "my.bucket", Region: "eu-west-2"}, "k", "https://s3.eu-west-2.amazonaws.com/my.bucket/k"},
		{Config{Bucket: "b", Endpoint: "https://acct.r2.cloudflarestorage.com", PathStyle: true}, "a b/c", "https://acct.r2.cloudflarestorage.com/b/a%20b/c"},
		{Config{Bucket: "b", Endpoint: "http://localhost:9000", PathStyle: true}, "", "http://localhost:9000/b"},
		{Config{Bucket: "b", Endpoint: "https://s3.wasabisys.com"}, "k", "https://b.s3.wasabisys.com/k"},
	}
	for _, c := range cases {
		s := &Store{cfg: c.cfg}
		if got := s.objectURL(c.key, nil); got != c.want {
			t.Errorf("%+v %q: got %s want %s", c.cfg, c.key, got, c.want)
		}
	}
}
