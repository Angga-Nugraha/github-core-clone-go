// Shared helpers: ids, base64url, JSON encoding, request ids.
package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const zeroOID = "0000000000000000000000000000000000000000"

// sha1Hex returns the hex SHA-1 of data.
func sha1Hex(data []byte) string {
	h := sha1.Sum(data)
	return hex.EncodeToString(h[:])
}

// objectID returns the git object id for type + content.
func objectID(typ string, content []byte) string {
	h := sha1.New()
	h.Write([]byte(typ))
	h.Write([]byte(" "))
	h.Write([]byte(strconv.Itoa(len(content))))
	h.Write([]byte{0})
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// b64url encodes without padding, like Node's Buffer.toString('base64url').
func b64url(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func b64decode(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, s)
	return base64.StdEncoding.DecodeString(clean)
}

// gid builds the opaque global ids used by GraphQL.
func gid(kind string, parts ...string) string {
	return b64url(kind + ":" + strings.Join(parts, ":"))
}

// reqID returns a fresh non-empty request id for X-GitHub-Request-Id.
var reqCounter uint64

func reqID() string {
	n := atomic.AddUint64(&reqCounter, 1)
	return fmt.Sprintf("req-%08x", n)
}

// jsonBytes marshals v to JSON without HTML escaping (like JSON.stringify).
func jsonBytes(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	// Encoder appends a newline; strip it (callers add their own).
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b
}

// jsonMap is a small alias for building JSON objects.
type jsonMap = map[string]any

// sortedKeys returns the map keys in byte order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseTimeMs parses an ISO-8601 timestamp to epoch milliseconds.
func parseTimeMs(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05Z", s)
		if err != nil {
			return 0
		}
	}
	return t.UnixMilli()
}

// isoFromMs formats epoch milliseconds as ISO-8601 UTC.
func isoFromMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

// clamp limits n to [lo, hi].
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// lastSegment returns the part after the last ':' (owner:branch -> branch).
func lastSegment(s string) string {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// splitPath splits a URL path on '/', dropping empty segments.
func splitPath(p string) []string {
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// isHex reports whether s is all lowercase/uppercase hex digits.
func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return len(s) > 0
}
