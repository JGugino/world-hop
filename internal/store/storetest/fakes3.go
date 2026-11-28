// fakes3.go: a fake S3 server for tests. It checks SigV4 signatures with its own
// implementation, so a signing bug in the real store fails a test instead of passing
// silently.

package storetest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// FakeS3 is an in-process S3-compatible server (path-style addressing) that
// verifies SigV4 signatures with its own implementation, so a signing bug in
// the store shows up as a 403 rather than passing silently.
type FakeS3 struct {
	*httptest.Server
	Bucket, AccessKey, SecretKey, Region string

	// PageSize caps keys per list response, to exercise pagination.
	PageSize int
	// Fail503 makes the next N requests fail with 503.
	Fail503 int
	// IgnoreIfNoneMatch models services that accept but ignore the header.
	IgnoreIfNoneMatch bool
	// NoConditional models services that answer 501 to If-None-Match.
	NoConditional bool

	mu      sync.Mutex
	objects map[string][]byte
	Puts    int // PUT requests that stored data
}

// NewFakeS3 returns an empty fake bucket that accepts requests signed with the given
// access key, secret key and region.
func NewFakeS3(bucket, access, secret, region string) *FakeS3 {
	f := &FakeS3{Bucket: bucket, AccessKey: access, SecretKey: secret, Region: region,
		PageSize: 1000, objects: map[string][]byte{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Keys lists the stored object keys, sorted.
func (f *FakeS3) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Put stores an object directly, bypassing the HTTP API (for seeding tests).
func (f *FakeS3) Put(key string, data []byte) {
	f.mu.Lock()
	f.objects[key] = data
	f.mu.Unlock()
}

// Get returns a stored object and whether it exists.
func (f *FakeS3) Get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return b, ok
}

// s3err writes an S3-style XML error response.
func s3err(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
}

// hm is HMAC-SHA256, used to recompute the signature.
func hm(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// enc percent-encodes a path the way SigV4 does.
func enc(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// verify checks the request's SigV4 Authorization header.
func (f *FakeS3) verify(r *http.Request, body []byte) (code, msg string) {
	auth := r.Header.Get("Authorization")
	rest, found := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 ")
	if !found {
		return "AccessDenied", "missing signature"
	}
	fields := map[string]string{}
	for _, p := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		fields[k] = v
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[4] != "aws4_request" || cred[3] != "s3" {
		return "AuthorizationHeaderMalformed", "bad credential"
	}
	if cred[0] != f.AccessKey {
		return "InvalidAccessKeyId", "unknown access key"
	}
	if cred[2] != f.Region {
		return "AuthorizationHeaderMalformed", "region is " + f.Region
	}
	payload := r.Header.Get("x-amz-content-sha256")
	if h := sha256.Sum256(body); hex.EncodeToString(h[:]) != payload {
		return "XAmzContentSHA256Mismatch", "payload hash does not match body"
	}
	var ch strings.Builder
	for _, n := range strings.Split(fields["SignedHeaders"], ";") {
		v := r.Header.Get(n)
		if n == "host" {
			v = r.Host
		}
		ch.WriteString(n + ":" + strings.Join(strings.Fields(v), " ") + "\n")
	}
	var qs []string
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			qs = append(qs, enc(k)+"="+enc(v))
		}
	}
	sort.Strings(qs)
	canon := strings.Join([]string{r.Method, r.URL.EscapedPath(), strings.Join(qs, "&"),
		ch.String(), fields["SignedHeaders"], payload}, "\n")
	ch2 := sha256.Sum256([]byte(canon))
	date := r.Header.Get("x-amz-date")
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", date, strings.Join(cred[1:], "/"), hex.EncodeToString(ch2[:])}, "\n")
	k := hm([]byte("AWS4"+f.SecretKey), cred[1])
	k = hm(k, cred[2])
	k = hm(k, "s3")
	k = hm(k, "aws4_request")
	if hex.EncodeToString(hm(k, toSign)) != fields["Signature"] {
		return "SignatureDoesNotMatch", "The request signature we calculated does not match"
	}
	return "", ""
}

// serve routes a request: it verifies the signature first (403 on a mismatch), then
// handles PUT (honouring If-None-Match), GET, HEAD and the list call.
func (f *FakeS3) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	if f.Fail503 > 0 {
		f.Fail503--
		f.mu.Unlock()
		s3err(w, 503, "SlowDown", "slow down")
		return
	}
	f.mu.Unlock()
	if code, msg := f.verify(r, body); code != "" {
		s3err(w, 403, code, msg)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != f.Bucket {
		s3err(w, 404, "NoSuchBucket", "The specified bucket does not exist")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case key == "" && r.Method == "GET":
		f.list(w, r)
	case r.Method == "PUT":
		if r.Header.Get("If-None-Match") == "*" {
			if f.NoConditional {
				s3err(w, 501, "NotImplemented", "conditional writes unsupported")
				return
			}
			if _, exists := f.objects[key]; exists && !f.IgnoreIfNoneMatch {
				s3err(w, 412, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold")
				return
			}
		}
		f.objects[key] = append([]byte(nil), body...)
		f.Puts++
		w.WriteHeader(200)
	case r.Method == "GET" || r.Method == "HEAD":
		b, ok := f.objects[key]
		if !ok {
			if r.Method == "HEAD" {
				w.WriteHeader(404)
				return
			}
			s3err(w, 404, "NoSuchKey", "The specified key does not exist.")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(200)
		if r.Method == "GET" {
			w.Write(b)
		}
	case r.Method == "DELETE":
		delete(f.objects, key) // like S3: 204 whether or not it existed
		w.WriteHeader(204)
	default:
		s3err(w, 405, "MethodNotAllowed", "no")
	}
}

// list answers ListObjectsV2 for a prefix, paged by max-keys and continuation token.
func (f *FakeS3) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix, after := q.Get("prefix"), q.Get("continuation-token")
	max := f.PageSize
	if n, err := strconv.Atoi(q.Get("max-keys")); err == nil && n > 0 && n < max {
		max = n
	}
	var keys []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type content struct {
		Key string `xml:"Key"`
	}
	res := struct {
		XMLName   xml.Name  `xml:"ListBucketResult"`
		Truncated bool      `xml:"IsTruncated"`
		Next      string    `xml:"NextContinuationToken,omitempty"`
		Contents  []content `xml:"Contents"`
	}{}
	if len(keys) > max {
		keys, res.Truncated = keys[:max], true
		res.Next = keys[max-1]
	}
	for _, k := range keys {
		res.Contents = append(res.Contents, content{k})
	}
	w.Header().Set("Content-Type", "application/xml")
	xml.NewEncoder(w).Encode(res)
}
