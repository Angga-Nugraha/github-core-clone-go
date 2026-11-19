// Entry point. Lifecycle: bind 127.0.0.1:0, register the port with the
// harness, poll the done endpoint until the trial ends, then exit 0.
// HTTP routing mirrors the Node app.ts: health, git smart HTTP, MCP,
// GraphQL, REST v3.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	s := newStore()
	mux := &http.ServeMux{}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		dispatch(s, w, r)
	})
	server := &http.Server{Handler: mux}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	envURL := os.Getenv("MAKEFUN_ENV_URL")
	if envURL == "" {
		fmt.Printf("listening on 127.0.0.1:%d\n", port)
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()

	// Register with the harness.
	registerURL := strings.TrimSuffix(envURL, "/") + "/register"
	doneURL := strings.TrimSuffix(envURL, "/") + "/done"
	client := &http.Client{Timeout: 10 * time.Second}
	body, _ := json.Marshal(map[string]int{"port": port})
	resp, err := client.Post(registerURL, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "register failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	for {
		time.Sleep(500 * time.Millisecond)
		resp, err := client.Get(doneURL)
		if err != nil {
			continue
		}
		var payload struct {
			Done bool `json:"done"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if payload.Done {
			break
		}
	}
	server.Close()
	os.Exit(0)
}

func isAuthorized(s *store, r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(header[len("Bearer "):])
	return token != "" && token == s.token
}

// writeResp writes a fully-built response with the standard headers.
func writeResp(w http.ResponseWriter, r resp, withReqID bool) {
	if withReqID {
		w.Header().Set("X-GitHub-Request-Id", reqID())
	}
	for k, v := range r.headers {
		w.Header().Set(k, v)
	}
	if r.contentType != "" {
		w.Header().Set("Content-Type", r.contentType)
	}
	if r.body != nil {
		w.Header().Set("Content-Length", strconv.Itoa(len(r.body)))
	}
	w.WriteHeader(r.status)
	if len(r.body) > 0 {
		w.Write(r.body)
	}
}

func dispatch(s *store, w http.ResponseWriter, r *http.Request) {
	parsedURL, err := url.ParseRequestURI(r.URL.RequestURI())
	if err != nil {
		parsedURL = &url.URL{Path: r.URL.Path}
	}
	pathname := parsedURL.Path
	segments := splitPath(pathname)
	method := r.Method

	// Health check: no auth.
	if method == "GET" && pathname == "/healthz" {
		writeResp(w, jsonResp(200, jsonMap{"ok": true}), true)
		return
	}

	// Git smart HTTP: /{owner}/{repo}.git/...
	if len(segments) >= 2 && strings.HasSuffix(segments[1], ".git") &&
		((method == "GET" && len(segments) > 2 && segments[2] == "info") ||
			(method == "POST" && len(segments) > 2 && (segments[2] == "git-upload-pack" || segments[2] == "git-receive-pack"))) {
		var body []byte
		if method == "POST" {
			body, _ = io.ReadAll(r.Body)
		}
		v2 := r.Header.Get("Git-Protocol") == "version=2"
		isWrite := method == "POST" && len(segments) > 2 && segments[2] == "git-receive-pack"
		if isWrite {
			s.mu.Lock()
		} else {
			s.mu.RLock()
		}
		status, ct, out := handleGitHTTP(s, segments, parsedURL.Query(), method, body, v2)
		if isWrite {
			s.mu.Unlock()
		} else {
			s.mu.RUnlock()
		}
		writeResp(w, resp{status: status, contentType: ct, body: out}, true)
		return
	}

	// MCP.
	if pathname == "/mcp" {
		if method == "DELETE" {
			writeResp(w, jsonResp(200, jsonMap{}), true)
			return
		}
		if method != "POST" {
			writeResp(w, errResp(405, "Method Not Allowed"), true)
			return
		}
		if !isAuthorized(s, r) {
			writeResp(w, errResp(401, "Unauthorized"), true)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var parsed any
		if err := json.Unmarshal(body, &parsed); err != nil {
			writeResp(w, resp{status: 400, contentType: "application/json", body: []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`)}, true)
			return
		}
		s.mu.Lock()
		status, out := handleMcp(s, parsed)
		s.mu.Unlock()
		if out == nil {
			// 202 empty (notifications): bare status, no headers.
			w.WriteHeader(status)
			return
		}
		writeResp(w, resp{status: status, contentType: "application/json", body: out}, true)
		return
	}

	// GraphQL.
	if pathname == "/api/graphql" {
		if method != "POST" {
			writeResp(w, errResp(405, "Method Not Allowed"), true)
			return
		}
		if !isAuthorized(s, r) {
			writeResp(w, errResp(401, "Unauthorized"), true)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var parsed any
		if err := json.Unmarshal(body, &parsed); err != nil {
			writeResp(w, errResp(400, "Invalid JSON"), true)
			return
		}
		operations := []any{parsed}
		isBatch := false
		if arr, ok := parsed.([]any); ok {
			operations = arr
			isBatch = true
		}
		s.mu.Lock()
		results := make([]any, 0, len(operations))
		for _, op := range operations {
			rec, _ := op.(map[string]any)
			query, _ := rec["query"].(string)
			vars, _ := rec["variables"].(map[string]any)
			if vars == nil {
				vars = map[string]any{}
			}
			opName, _ := rec["operationName"].(string)
			results = append(results, executeGraphql(s, query, vars, opName))
		}
		s.mu.Unlock()
		if isBatch {
			writeResp(w, jsonResp(200, results), true)
		} else {
			writeResp(w, jsonResp(200, results[0]), true)
		}
		return
	}

	// REST v3.
	if pathname == "/api/v3" || strings.HasPrefix(pathname, "/api/v3/") {
		if !isAuthorized(s, r) {
			writeResp(w, errResp(401, "Unauthorized"), true)
			return
		}
		restSegments := segments[2:]
		var body []byte
		bodyValid := true
		if method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE" {
			body, _ = io.ReadAll(r.Body)
			if len(body) > 0 {
				var probe any
				if err := json.Unmarshal(body, &probe); err != nil {
					bodyValid = false
				}
			}
		}
		origin := "http://" + r.Host
		if method == "GET" {
			s.mu.RLock()
			res := handleRest(s, restSegments, parsedURL.Query(), method, origin, pathname, body, bodyValid)
			s.mu.RUnlock()
			writeResp(w, res, true)
		} else {
			s.mu.Lock()
			res := handleRest(s, restSegments, parsedURL.Query(), method, origin, pathname, body, bodyValid)
			s.mu.Unlock()
			writeResp(w, res, true)
		}
		return
	}

	writeResp(w, errResp(404, "Not Found"), true)
}
