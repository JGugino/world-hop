// Package s3 is a store.Store on any S3-compatible object store: AWS S3,
// Cloudflare R2, Backblaze B2, MinIO, Wasabi and so on. It speaks the S3 REST
// API directly with SigV4, so it needs no SDK.
//
// Layout matches the Google Drive store: "packs/<sha256>" and
// "worlds/<id>/<manifest>", under an optional key prefix. The store is
// byte-agnostic: it stores and returns exactly the bytes it is given, so
// anything that transforms packs or manifests (compression, encryption) sits
// above it. Verifying a pack against its hash is the sync engine's job.
package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JGugino/world-hopper/internal/httpx"
	"github.com/JGugino/world-hopper/internal/manifest"
	"github.com/JGugino/world-hopper/internal/store"
)

// Config says where the bucket is and how to sign in to it.
type Config struct {
	// Endpoint is the service URL, e.g. "https://<account>.r2.cloudflarestorage.com".
	// Empty means AWS S3 in Region. A bare host gets "https://".
	Endpoint string
	// Region is used for signing; default "us-east-1". Many S3-compatible
	// services accept any value ("auto" for R2).
	Region string
	Bucket string
	// Prefix is an optional folder inside the bucket, e.g. "worldhop".
	Prefix string
	// PathStyle addresses the bucket as host/bucket/key instead of
	// bucket.host/key. Needed by MinIO and most self-hosted services.
	PathStyle bool

	AccessKey string
	SecretKey string
}

// Normalize fills defaults and tidies fields, returning an error for
// anything unusable. It never contacts the network.
func (c Config) Normalize() (Config, error) {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Region = strings.TrimSpace(c.Region)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.Bucket == "" {
		return c, errors.New("s3: bucket is required")
	}
	if strings.ContainsAny(c.Bucket, "/ ") {
		return c, errors.New("s3: bucket must be a name only, without slashes (use the prefix field for a folder)")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return c, errors.New("s3: access key and secret key are required")
	}
	if c.Endpoint != "" {
		if !strings.Contains(c.Endpoint, "://") {
			c.Endpoint = "https://" + c.Endpoint
		}
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return c, fmt.Errorf("s3: bad endpoint %q", c.Endpoint)
		}
		c.Endpoint = strings.TrimRight(u.Scheme+"://"+u.Host, "/")
	}
	return c, nil
}

// Store implements store.Store over S3.
type Store struct {
	cfg Config
	hc  *http.Client
	// Now is the clock used for signing (tests).
	Now func() time.Time
}

var _ store.Store = (*Store)(nil)

// New returns a Store. hc may be nil. Redirects are not followed, because a
// redirected request would no longer match its signature.
func New(cfg Config, hc *http.Client) (*Store, error) {
	cfg, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Store{cfg: cfg, hc: &c, Now: time.Now}, nil
}

func packKey(name string) string        { return "packs/" + name }
func manifestKey(w, name string) string { return "worlds/" + w + "/" + name }

// objectURL builds the request URL for a key ("" for the bucket itself).
func (s *Store) objectURL(key string, q url.Values) string {
	base := s.cfg.Endpoint
	if base == "" {
		base = "https://s3." + s.cfg.Region + ".amazonaws.com"
		if s.cfg.Region == "us-east-1" {
			base = "https://s3.amazonaws.com"
		}
	}
	u, _ := url.Parse(base)
	host, path := u.Host, ""
	// Dots in a bucket name break TLS for virtual-hosted style on AWS.
	if s.cfg.PathStyle || strings.Contains(s.cfg.Bucket, ".") && u.Scheme == "https" {
		path = "/" + s.cfg.Bucket
	} else {
		host = s.cfg.Bucket + "." + host
	}
	if key != "" {
		path += "/" + key
	}
	if path == "" {
		path = "/"
	}
	out := u.Scheme + "://" + host + escapePath(path)
	if len(q) > 0 {
		out += "?" + canonicalQuery(q)
	}
	return out
}

func escapePath(p string) string { return uriEncode(p, true) }

func (s *Store) key(k string) string {
	if s.cfg.Prefix == "" {
		return k
	}
	return s.cfg.Prefix + "/" + k
}

// do signs and sends one request, with retry, and returns the response
// unchecked. body may be nil.
func (s *Store) do(ctx context.Context, method, key string, q url.Values, body []byte, hdr map[string]string) (*http.Response, error) {
	target := s.objectURL(key, q)
	payload := emptyPayloadSHA
	if len(body) > 0 {
		payload = sum(body)
	}
	return httpx.Do(ctx, s.hc, func() (*http.Request, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, target, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		sign(req, s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, payload, s.Now())
		return req, nil
	})
}

// Error is an S3 error response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	var hint string
	switch e.Code {
	case "SignatureDoesNotMatch":
		hint = " (check the secret key, region and endpoint)"
	case "InvalidAccessKeyId":
		hint = " (check the access key)"
	case "NoSuchBucket":
		hint = " (check the bucket name and endpoint)"
	case "AccessDenied":
		hint = " (the key needs list, read and write access to this bucket)"
	case "PermanentRedirect", "AuthorizationHeaderMalformed":
		hint = " (the bucket is probably in a different region)"
	}
	if e.Code == "" {
		return fmt.Sprintf("s3: http %d", e.Status)
	}
	return fmt.Sprintf("s3: %s: %s%s", e.Code, e.Message, hint)
}

func readError(resp *http.Response) error {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	e := &Error{Status: resp.StatusCode}
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(b, &x) == nil {
		e.Code, e.Message = x.Code, x.Message
	}
	if e.Code == "" {
		switch resp.StatusCode {
		case 301, 307:
			e.Code, e.Message = "PermanentRedirect", "endpoint or region is wrong"
		case 403:
			e.Code, e.Message = "AccessDenied", "access denied"
		case 404:
			e.Code, e.Message = "NotFound", "not found"
		}
	}
	return e
}

func ok(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode < 300 }

// head returns the object's size, or ok=false if it does not exist.
func (s *Store) head(ctx context.Context, key string) (size int64, found bool, err error) {
	resp, err := s.do(ctx, "HEAD", s.key(key), nil, nil, nil)
	if err != nil {
		return 0, false, err
	}
	resp.Body.Close()
	switch {
	case ok(resp):
		return resp.ContentLength, true, nil
	case resp.StatusCode == http.StatusNotFound:
		return 0, false, nil
	}
	// HEAD has no body, so synthesise the error from the status alone.
	return 0, false, readError(resp)
}

func (s *Store) get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.do(ctx, "GET", s.key(key), nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		e := readError(resp).(*Error)
		if e.Code == "NoSuchBucket" {
			return nil, e
		}
		return nil, store.ErrNotFound
	}
	if !ok(resp) {
		return nil, readError(resp)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (s *Store) put(ctx context.Context, key string, data []byte, hdr map[string]string) (*http.Response, error) {
	if data == nil {
		data = []byte{}
	}
	return s.do(ctx, "PUT", s.key(key), nil, data, hdr)
}

func (s *Store) PutPack(ctx context.Context, name string, data []byte) error {
	if !store.ValidPackName(name) {
		return fmt.Errorf("s3: bad pack name %q", name)
	}
	// Packs are named by content, so a same-sized object is already this pack.
	// Comparing sizes is enough here because the engine verifies hashes on pull.
	if size, found, err := s.head(ctx, packKey(name)); err != nil {
		return err
	} else if found && size == int64(len(data)) {
		return nil
	}
	resp, err := s.put(ctx, packKey(name), data, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	if !ok(resp) {
		return readError(resp)
	}
	resp.Body.Close()
	return nil
}

func (s *Store) GetPack(ctx context.Context, name string) ([]byte, error) {
	if !store.ValidPackName(name) {
		return nil, fmt.Errorf("s3: bad pack name %q", name)
	}
	return s.get(ctx, packKey(name))
}

// PutManifest never overwrites. It sends If-None-Match: * so the service
// enforces that, after a HEAD so that services which ignore the header (some
// older S3-compatible ones) still refuse an existing name.
func (s *Store) PutManifest(ctx context.Context, world, name string, data []byte) error {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return fmt.Errorf("s3: bad manifest name %q/%q", world, name)
	}
	key := manifestKey(world, name)
	if _, found, err := s.head(ctx, key); err != nil {
		return err
	} else if found {
		return store.ErrExists
	}
	hdr := map[string]string{"Content-Type": "application/json", "If-None-Match": "*"}
	resp, err := s.put(ctx, key, data, hdr)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotImplemented { // conditional writes unsupported
		resp.Body.Close()
		delete(hdr, "If-None-Match")
		if resp, err = s.put(ctx, key, data, hdr); err != nil {
			return err
		}
	}
	switch {
	case ok(resp):
		resp.Body.Close()
		return nil
	case resp.StatusCode == http.StatusPreconditionFailed:
		resp.Body.Close()
		return store.ErrExists
	}
	return readError(resp)
}

func (s *Store) GetManifest(ctx context.Context, world, name string) ([]byte, error) {
	if !manifest.ValidID(world) || !store.ValidManifestName(name) {
		return nil, fmt.Errorf("s3: bad manifest name %q/%q", world, name)
	}
	return s.get(ctx, manifestKey(world, name))
}

type listResult struct {
	IsTruncated bool   `xml:"IsTruncated"`
	NextToken   string `xml:"NextContinuationToken"`
	Contents    []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
}

// list returns every key under prefix (relative to the store's prefix),
// following continuation tokens. max, if > 0, caps one page (and, with
// firstOnly, the whole call).
func (s *Store) list(ctx context.Context, prefix string, pageSize int, firstOnly bool) ([]string, error) {
	var out []string
	token := ""
	strip := ""
	if s.cfg.Prefix != "" {
		strip = s.cfg.Prefix + "/"
	}
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {s.key(prefix)}}
		if pageSize > 0 {
			q.Set("max-keys", strconv.Itoa(pageSize))
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := s.do(ctx, "GET", "", q, nil, nil)
		if err != nil {
			return nil, err
		}
		if !ok(resp) {
			return nil, readError(resp)
		}
		var res listResult
		err = xml.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: bad list response: %w", err)
		}
		for _, c := range res.Contents {
			out = append(out, strings.TrimPrefix(c.Key, strip))
		}
		if firstOnly || !res.IsTruncated || res.NextToken == "" {
			return out, nil
		}
		token = res.NextToken
	}
}

// Test checks that the bucket is reachable and the credentials may list it.
func (s *Store) Test(ctx context.Context) error {
	_, err := s.list(ctx, "", 1, true)
	return err
}

func (s *Store) ListManifests(ctx context.Context, world string) ([]string, error) {
	if !manifest.ValidID(world) {
		return nil, fmt.Errorf("s3: bad world id %q", world)
	}
	prefix := "worlds/" + world + "/"
	keys, err := s.list(ctx, prefix, 0, false)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, k := range keys {
		if n := strings.TrimPrefix(k, prefix); n != k && store.ValidManifestName(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) ListWorlds(ctx context.Context) ([]string, error) {
	keys, err := s.list(ctx, "worlds/", 0, false)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		rest := strings.TrimPrefix(k, "worlds/")
		id, name, found := strings.Cut(rest, "/")
		if found && manifest.ValidID(id) && store.ValidManifestName(name) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}
