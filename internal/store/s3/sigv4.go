// sigv4.go: AWS Signature Version 4 request signing, implemented here instead of
// pulling in the AWS SDK. Every S3 request is signed so the service knows it came from
// the holder of the secret key.

package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	amzDateLayout   = "20060102T150405Z"
	emptyPayloadSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// sum is the SHA-256 of b as hex (the payload hash SigV4 signs).
func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hmacSHA256 is HMAC-SHA256 of s under key, the building block of SigV4's key
// derivation and signature.
func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

// uriEncode percent-encodes per SigV4: everything except unreserved
// characters, and '/' only when keepSlash is set (object key paths).
func uriEncode(s string, keepSlash bool) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&15])
		}
	}
	return b.String()
}

// canonicalQuery sorts and encodes query parameters the way SigV4 requires, so the
// signer and the service see an identical string.
func canonicalQuery(v url.Values) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), v[k]...)
		sort.Strings(vs)
		for _, x := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(x, false))
		}
	}
	return strings.Join(parts, "&")
}

// sign adds x-amz-date, x-amz-content-sha256 and Authorization to req using
// AWS Signature Version 4 for the S3 service. Every header already on req is
// signed, along with Host. payloadHash is the hex SHA-256 of the body.
// The request URL's path must already be in its final, escaped form.
func sign(req *http.Request, accessKey, secretKey, region, payloadHash string, now time.Time) {
	now = now.UTC()
	date := now.Format("20060102")
	req.Header.Set("x-amz-date", now.Format(amzDateLayout))
	req.Header.Set("x-amz-content-sha256", payloadHash)

	names := []string{"host"}
	vals := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "authorization" {
			continue
		}
		names = append(names, lk)
		vals[lk] = strings.Join(v, ",")
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, n := range names {
		canonHeaders.WriteString(n + ":" + strings.Join(strings.Fields(vals[n]), " ") + "\n")
	}
	signed := strings.Join(names, ";")

	// S3 does not normalise or double-encode the path: it is encoded once.
	canonURI := req.URL.EscapedPath()
	if canonURI == "" {
		canonURI = "/"
	}
	canonical := strings.Join([]string{
		req.Method, canonURI, canonicalQuery(req.URL.Query()),
		canonHeaders.String(), signed, payloadHash,
	}, "\n")

	scope := date + "/" + region + "/s3/aws4_request"
	toSign := strings.Join([]string{algorithm, now.Format(amzDateLayout), scope, sum([]byte(canonical))}, "\n")

	k := hmacSHA256([]byte("AWS4"+secretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))

	req.Header.Set("Authorization", algorithm+" Credential="+accessKey+"/"+scope+
		",SignedHeaders="+signed+",Signature="+sig)
}
