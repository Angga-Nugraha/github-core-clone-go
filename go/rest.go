// REST v3 API under /api/v3. Mirrors the Node rest.ts behavior.
package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const jsonCT = "application/json; charset=utf-8"

// resp is a fully-built HTTP response (computed under the store lock,
// written after the lock is released).
type resp struct {
	status      int
	contentType string
	body        []byte
	headers     map[string]string
}

func jsonResp(status int, v any) resp {
	return resp{status: status, contentType: jsonCT, body: append(jsonBytes(v), '\n')}
}

func jsonTextResp(status int, text []byte) resp {
	return resp{status: status, contentType: jsonCT, body: text}
}

func errJSON(msg string) []byte {
	return append(jsonBytes(jsonMap{"message": msg}), '\n')
}

func errResp(status int, msg string) resp {
	return resp{status: status, contentType: jsonCT, body: errJSON(msg)}
}

func noContentResp() resp { return resp{status: 204, contentType: "", body: nil} }

// ---------- serializers ----------

func repoGlobalID(r *repo) string { return gid("R", strconv.Itoa(r.ID)) }

func repoJSON(s *store, r *repo) jsonMap {
	open := 0
	for _, it := range r.Issues {
		if it.State == "open" {
			open++
		}
	}
	return jsonMap{
		"id":                r.ID,
		"node_id":           repoGlobalID(r),
		"name":              r.Name,
		"full_name":         r.FullName,
		"owner":             jsonMap{"login": r.Owner, "id": s.viewer.ID, "type": "User"},
		"private":           r.Private,
		"description":       r.Description,
		"default_branch":    r.DefaultBranch,
		"created_at":        r.CreatedAt,
		"updated_at":        r.UpdatedAt,
		"pushed_at":         r.UpdatedAt,
		"html_url":          "https://example.invalid/" + r.FullName,
		"url":               "/api/v3/repos/" + r.Owner + "/" + r.Name,
		"has_issues":        true,
		"has_projects":      false,
		"has_wiki":          false,
		"forks_count":       0,
		"stargazers_count":  0,
		"watchers_count":    0,
		"open_issues_count": open,
	}
}

func labelJSON(l *label) jsonMap {
	return jsonMap{
		"id": l.ID, "node_id": "L_" + strconv.Itoa(l.ID),
		"name": l.Name, "color": l.Color, "description": l.Description,
	}
}

func issueGlobalID(r *repo, it *issue) string {
	return gid("I", strconv.Itoa(r.ID), strconv.Itoa(it.Number))
}

func issueLabelsJSON(s *store, r *repo, it *issue) []any {
	out := make([]any, 0, len(it.Labels))
	for _, name := range it.Labels {
		if l, ok := r.Labels[name]; ok {
			out = append(out, labelJSON(l))
		} else {
			out = append(out, jsonMap{"id": 0, "node_id": "L_0", "name": name, "color": "ededed", "description": ""})
		}
	}
	return out
}

func issueJSONObject(s *store, r *repo, it *issue) jsonMap {
	var closedAt any
	if it.ClosedAt != "" {
		closedAt = it.ClosedAt
	}
	return jsonMap{
		"id": it.ID, "node_id": issueGlobalID(r, it),
		"number": it.Number, "title": it.Title, "body": it.Body, "state": it.State,
		"labels":     issueLabelsJSON(s, r, it),
		"user":       jsonMap{"login": s.viewer.Login, "id": s.viewer.ID, "type": "User"},
		"created_at": it.CreatedAt, "updated_at": it.UpdatedAt, "closed_at": closedAt,
		"html_url":     "https://example.invalid/" + r.FullName + "/issues/" + strconv.Itoa(it.Number),
		"url":          "/api/v3/repos/" + r.Owner + "/" + r.Name + "/issues/" + strconv.Itoa(it.Number),
		"comments":     len(it.Comments),
		"comments_url": "/api/v3/repos/" + r.Owner + "/" + r.Name + "/issues/" + strconv.Itoa(it.Number) + "/comments",
	}
}

// issueJSONBytes returns the cached serialization (with trailing newline).
func issueJSONBytes(s *store, r *repo, it *issue) []byte {
	if it.jsonCache == nil {
		it.jsonCache = append(jsonBytes(issueJSONObject(s, r, it)), '\n')
	}
	return it.jsonCache
}

func pullGlobalID(r *repo, p *pull) string {
	return gid("P", strconv.Itoa(r.ID), strconv.Itoa(p.Number))
}

func pullJSON(s *store, r *repo, p *pull) jsonMap {
	mergeable := false
	if p.State == "open" {
		mergeable = !s.prConflict(r, p)
	}
	var mergeSHA, mergedAt any
	if p.MergeCommit != "" {
		mergeSHA = p.MergeCommit
	}
	if p.MergedAt != "" {
		mergedAt = p.MergedAt
	}
	return jsonMap{
		"id": p.ID, "node_id": pullGlobalID(r, p),
		"number": p.Number, "title": p.Title, "body": p.Body, "state": p.State,
		"head":             jsonMap{"ref": p.Head, "sha": p.HeadSHA, "label": r.Owner + ":" + p.Head},
		"base":             jsonMap{"ref": p.Base, "sha": p.BaseSHA, "label": r.Owner + ":" + p.Base},
		"merged":           p.Merged,
		"mergeable":        mergeable,
		"merge_commit_sha": mergeSHA,
		"merged_at":        mergedAt,
		"user":             jsonMap{"login": s.viewer.Login, "id": s.viewer.ID, "type": "User"},
		"created_at":       p.CreatedAt, "updated_at": p.UpdatedAt,
		"html_url": "https://example.invalid/" + r.FullName + "/pull/" + strconv.Itoa(p.Number),
		"url":      "/api/v3/repos/" + r.Owner + "/" + r.Name + "/pulls/" + strconv.Itoa(p.Number),
	}
}

func commentJSON(s *store, r *repo, it *issue, c *comment) jsonMap {
	return jsonMap{
		"id": c.ID, "node_id": gid("C", strconv.Itoa(r.ID), strconv.Itoa(it.Number), strconv.Itoa(c.ID)),
		"body":       c.Body,
		"user":       jsonMap{"login": s.viewer.Login, "id": s.viewer.ID, "type": "User"},
		"created_at": c.CreatedAt, "updated_at": c.CreatedAt,
		"html_url": "https://example.invalid/" + r.FullName + "/issues/" + strconv.Itoa(it.Number) + "#issuecomment-" + strconv.Itoa(c.ID),
		"url":      "/api/v3/repos/" + r.Owner + "/" + r.Name + "/issues/" + strconv.Itoa(it.Number) + "/comments/" + strconv.Itoa(c.ID),
	}
}

// ---------- pagination ----------

func pageParams(query url.Values) (page, perPage int) {
	page, _ = strconv.Atoi(query.Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ = strconv.Atoi(query.Get("per_page"))
	if perPage < 1 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	return page, perPage
}

func linkHeader(origin, path string, page, perPage int, hasMore bool, totalPages int) string {
	if !hasMore && totalPages <= 1 {
		return ""
	}
	base := origin + path
	var parts []string
	if hasMore {
		parts = append(parts, "<"+base+"?page="+strconv.Itoa(page+1)+"&per_page="+strconv.Itoa(perPage)+">; rel=\"next\"")
	}
	if totalPages > 1 {
		parts = append(parts, "<"+base+"?page="+strconv.Itoa(totalPages)+"&per_page="+strconv.Itoa(perPage)+">; rel=\"last\"")
	}
	if page > 1 {
		parts = append(parts, "<"+base+"?page=1&per_page="+strconv.Itoa(perPage)+">; rel=\"first\"")
	}
	return strings.Join(parts, ", ")
}

func paginate[T any](items []T, page, perPage int) (pageItems []T, hasMore bool, totalPages int) {
	totalPages = (len(items) + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	start := (page - 1) * perPage
	if start >= len(items) {
		return []T{}, page < totalPages, totalPages
	}
	end := start + perPage
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], page < totalPages, totalPages
}

// ---------- body parsing ----------

func bodyObject(body []byte) jsonMap {
	if len(body) == 0 {
		return jsonMap{}
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return jsonMap{}
}

func stringField(body jsonMap, key string) string {
	if body == nil {
		return ""
	}
	if v, ok := body[key].(string); ok {
		return v
	}
	return ""
}

func boolField(body jsonMap, key string) bool {
	if body == nil {
		return false
	}
	b, _ := body[key].(bool)
	return b
}

// ---------- handlers ----------

// handleRest routes a REST v3 request. segments are the path parts after /api/v3.
// body is the raw request body (nil when absent); rawBodyValid reports JSON parse success.
func handleRest(s *store, segments []string, query url.Values, method, origin, path string, body []byte, bodyValid bool) resp {
	if !bodyValid {
		return errResp(400, "Invalid JSON")
	}
	parsed := bodyObject(body)

	if len(segments) == 0 {
		if method != "GET" {
			return errResp(404, "Not Found")
		}
		r := jsonResp(200, jsonMap{"current_user_url": "/api/v3/user", "rate_limit_url": "/api/v3/rate_limit"})
		r.headers = map[string]string{"x-oauth-scopes": "repo, read:org"}
		return r
	}

	if segments[0] == "meta" {
		if method != "GET" {
			return errResp(404, "Not Found")
		}
		return jsonResp(200, jsonMap{"installed_version": "1.0.0", "verifiable_password_authentication": false})
	}

	if segments[0] == "user" {
		if len(segments) == 1 {
			if method != "GET" {
				return errResp(404, "Not Found")
			}
			return jsonResp(200, s.userJSON())
		}
		if len(segments) == 2 && segments[1] == "repos" {
			if method == "GET" {
				page, perPage := pageParams(query)
				all := s.byOwner[s.viewer.Login]
				items, hasMore, totalPages := paginate(all, page, perPage)
				out := make([]any, 0, len(items))
				for _, r := range items {
					out = append(out, repoJSON(s, r))
				}
				r := jsonResp(200, out)
				if link := linkHeader(origin, path, page, perPage, hasMore, totalPages); link != "" {
					r.headers = map[string]string{"link": link}
				}
				return r
			}
			if method == "POST" {
				name := stringField(parsed, "name")
				if name == "" {
					return errResp(422, "name is required")
				}
				created, errMsg := s.createRepo(s.viewer.Login, name, stringField(parsed, "description"), boolField(parsed, "private"), "")
				if errMsg != "" {
					return errResp(422, errMsg)
				}
				return jsonResp(201, repoJSON(s, created))
			}
			return errResp(404, "Not Found")
		}
		return errResp(404, "Not Found")
	}

	if segments[0] != "repos" || len(segments) < 3 {
		return errResp(404, "Not Found")
	}
	owner, repoName := segments[1], segments[2]
	r := s.findRepo(owner, repoName)
	tail := segments[3:]

	if len(tail) == 0 {
		if method == "GET" {
			if r == nil {
				return errResp(404, "Not Found")
			}
			return jsonResp(200, repoJSON(s, r))
		}
		if method == "PATCH" {
			if r == nil {
				return errResp(404, "Not Found")
			}
			if v, ok := parsed["description"].(string); ok {
				r.Description = v
			}
			if v, ok := parsed["private"].(bool); ok {
				r.Private = v
			}
			r.UpdatedAt = s.nowISO()
			return jsonResp(200, repoJSON(s, r))
		}
		if method == "DELETE" {
			if r == nil {
				return errResp(404, "Not Found")
			}
			s.deleteRepo(r)
			return noContentResp()
		}
		return errResp(404, "Not Found")
	}
	if r == nil {
		return errResp(404, "Not Found")
	}

	head := tail[0]

	if head == "branches" {
		if len(tail) == 1 && method == "GET" {
			names := []string{}
			for ref := range r.Refs {
				if strings.HasPrefix(ref, "refs/heads/") {
					names = append(names, ref[len("refs/heads/"):])
				}
			}
			sort.Strings(names)
			out := make([]any, 0, len(names))
			for _, name := range names {
				out = append(out, jsonMap{"name": name, "commit": jsonMap{"sha": r.Refs["refs/heads/"+name]}, "protected": false})
			}
			return jsonResp(200, out)
		}
		if len(tail) == 2 && method == "GET" {
			tip, ok := r.Refs["refs/heads/"+tail[1]]
			if !ok {
				return errResp(404, "Not Found")
			}
			return jsonResp(200, jsonMap{"name": tail[1], "commit": jsonMap{"sha": tip}, "protected": false})
		}
		return errResp(404, "Not Found")
	}

	if head == "commits" {
		if method != "GET" || len(tail) != 1 {
			return errResp(404, "Not Found")
		}
		tip := r.resolveRef(query.Get("sha"))
		if tip == "" {
			return jsonResp(200, []any{})
		}
		page, perPage := pageParams(query)
		oids := r.commitsFrom(tip)
		all := make([]any, 0, len(oids))
		for _, oid := range oids {
			c := parsedCommit(r, oid)
			if c == nil {
				continue
			}
			author := jsonMap{"name": c.Author, "email": c.Email, "date": isoFromMs(c.Time * 1000)}
			all = append(all, jsonMap{
				"sha": oid,
				"commit": jsonMap{
					"message":   c.Message,
					"author":    author,
					"committer": author,
				},
				"author":    jsonMap{"login": s.viewer.Login, "id": s.viewer.ID, "type": "User"},
				"committer": jsonMap{"login": s.viewer.Login, "id": s.viewer.ID, "type": "User"},
			})
		}
		items, hasMore, totalPages := paginate(all, page, perPage)
		res := jsonResp(200, items)
		if link := linkHeader(origin, path, page, perPage, hasMore, totalPages); link != "" {
			res.headers = map[string]string{"link": link}
		}
		return res
	}

	if head == "git" && len(tail) > 1 && tail[1] == "refs" {
		if method == "POST" && len(tail) == 2 {
			ref := stringField(parsed, "ref")
			sha := stringField(parsed, "sha")
			if !strings.HasPrefix(ref, "refs/") || sha == "" {
				return errResp(422, "ref and sha are required")
			}
			if _, ok := r.Refs[ref]; ok {
				return errResp(422, "Reference already exists")
			}
			obj := r.getObject(sha)
			if obj == nil {
				return errResp(422, "sha does not exist")
			}
			r.Refs[ref] = sha
			return jsonResp(201, jsonMap{"ref": ref, "object": jsonMap{"sha": sha, "type": obj.Type, "url": ""}})
		}
		if method == "GET" && len(tail) > 2 {
			ref := "refs/" + strings.Join(tail[2:], "/")
			tip, ok := r.Refs[ref]
			if !ok {
				return errResp(404, "Not Found")
			}
			return jsonResp(200, jsonMap{"ref": ref, "object": jsonMap{"sha": tip, "type": "commit", "url": ""}})
		}
		return errResp(404, "Not Found")
	}

	if head == "contents" {
		contentPath := strings.Join(tail[1:], "/")
		if method == "GET" {
			tip := r.resolveRef(query.Get("ref"))
			if tip == "" {
				return errResp(404, "Not Found")
			}
			c := parsedCommit(r, tip)
			if c == nil {
				return errResp(404, "Not Found")
			}
			if contentPath == "" {
				return jsonResp(200, dirEntries(s, r, c.Tree, ""))
			}
			if dir := r.listDir(c.Tree, contentPath); dir != nil {
				return jsonResp(200, dirEntries(s, r, c.Tree, contentPath))
			}
			oid := r.lookupPath(c.Tree, contentPath)
			if oid == "" {
				return errResp(404, "Not Found")
			}
			obj := r.getObject(oid)
			if obj == nil || obj.Type != "blob" {
				return errResp(404, "Not Found")
			}
			name := contentPath
			if i := strings.LastIndex(contentPath, "/"); i >= 0 {
				name = contentPath[i+1:]
			}
			return jsonResp(200, jsonMap{
				"type": "file", "encoding": "base64", "size": len(obj.Content),
				"name": name, "path": contentPath,
				"content":  base64.StdEncoding.EncodeToString(obj.Content),
				"sha":      oid,
				"url":      "/api/v3/repos/" + owner + "/" + repoName + "/contents/" + contentPath,
				"html_url": "https://example.invalid/" + r.FullName + "/blob/" + r.DefaultBranch + "/" + contentPath,
				"git_url":  "", "download_url": "",
			})
		}
		if method == "PUT" {
			contentRaw, ok := parsed["content"].(string)
			if !ok {
				return errResp(422, "content is required")
			}
			content, _ := b64decode(strings.ReplaceAll(strings.ReplaceAll(contentRaw, "\n", ""), " ", ""))
			branch := ""
			if v, ok := parsed["branch"].(string); ok {
				branch = v
			}
			sha := ""
			if v, ok := parsed["sha"].(string); ok {
				sha = v
			}
			commitSHA, contentSHA, created, status, errMsg := s.putContent(r, contentPath, branch, string(content), stringField(parsed, "message"), sha)
			if errMsg != "" {
				return errResp(status, errMsg)
			}
			code := 200
			if created {
				code = 201
			}
			name := contentPath
			if i := strings.LastIndex(contentPath, "/"); i >= 0 {
				name = contentPath[i+1:]
			}
			return jsonResp(code, jsonMap{
				"content": jsonMap{"name": name, "path": contentPath, "sha": contentSHA, "size": len(content), "type": "file", "encoding": "base64", "url": ""},
				"commit":  jsonMap{"sha": commitSHA, "message": stringField(parsed, "message")},
			})
		}
		if method == "DELETE" {
			branch := ""
			if v, ok := parsed["branch"].(string); ok {
				branch = v
			}
			commitSHA, status, errMsg := s.deleteContent(r, contentPath, branch, stringField(parsed, "message"))
			if errMsg != "" {
				return errResp(status, errMsg)
			}
			return jsonResp(200, jsonMap{"commit": jsonMap{"sha": commitSHA, "message": stringField(parsed, "message")}})
		}
		return errResp(404, "Not Found")
	}

	if head == "labels" {
		if method == "GET" {
			out := make([]any, 0, len(r.Labels))
			for _, l := range r.Labels {
				out = append(out, labelJSON(l))
			}
			return jsonResp(200, out)
		}
		if method == "POST" {
			name := stringField(parsed, "name")
			if name == "" {
				return errResp(422, "name is required")
			}
			if existing, ok := r.Labels[name]; ok {
				return jsonResp(201, labelJSON(existing))
			}
			color := "ededed"
			if v, ok := parsed["color"].(string); ok {
				color = v
			}
			desc := ""
			if v, ok := parsed["description"].(string); ok {
				desc = v
			}
			l := s.createLabel(r, name, color, desc)
			return jsonResp(201, labelJSON(l))
		}
		return errResp(404, "Not Found")
	}

	if head == "issues" {
		if len(tail) == 1 {
			if method == "GET" {
				page, perPage := pageParams(query)
				state := query.Get("state")
				if state == "" {
					state = "open"
				}
				desc := query.Get("direction") != "asc"
				all := r.listIssues(state, desc)
				items, hasMore, totalPages := paginate(all, page, perPage)
				out := make([]any, 0, len(items))
				for _, it := range items {
					out = append(out, issueJSONObject(s, r, it))
				}
				res := jsonResp(200, out)
				if link := linkHeader(origin, path, page, perPage, hasMore, totalPages); link != "" {
					res.headers = map[string]string{"link": link}
				}
				return res
			}
			if method == "POST" {
				title := stringField(parsed, "title")
				if title == "" {
					return errResp(422, "title is required")
				}
				var labelNames []string
				if arr, ok := parsed["labels"].([]any); ok {
					for _, e := range arr {
						switch v := e.(type) {
						case string:
							labelNames = append(labelNames, v)
						case map[string]any:
							if n, ok := v["name"].(string); ok {
								labelNames = append(labelNames, n)
							}
						}
					}
				}
				created, errMsg := s.createIssue(r, title, stringField(parsed, "body"), labelNames)
				if errMsg != "" {
					return errResp(422, errMsg)
				}
				return jsonResp(201, issueJSONObject(s, r, created))
			}
			return errResp(404, "Not Found")
		}
		number, err := strconv.Atoi(tail[1])
		if err != nil {
			return errResp(404, "Not Found")
		}
		it := s.findIssue(r, number)
		if len(tail) == 2 {
			if it == nil {
				return errResp(404, "Not Found")
			}
			if method == "GET" {
				return jsonTextResp(200, issueJSONBytes(s, r, it))
			}
			if method == "PATCH" {
				if v, ok := parsed["state"].(string); ok && (v == "open" || v == "closed") {
					it.State = v
					if v == "closed" {
						it.ClosedAt = s.nowISO()
					} else {
						it.ClosedAt = ""
					}
				}
				if v, ok := parsed["title"].(string); ok {
					it.Title = v
				}
				if v, ok := parsed["body"].(string); ok {
					it.Body = v
				}
				it.UpdatedAt = s.nowISO()
				it.jsonCache = nil
				return jsonResp(200, issueJSONObject(s, r, it))
			}
			return errResp(404, "Not Found")
		}
		if len(tail) == 3 && tail[2] == "comments" {
			if it == nil {
				return errResp(404, "Not Found")
			}
			if method == "GET" {
				out := make([]any, 0, len(it.Comments))
				for _, c := range it.Comments {
					out = append(out, commentJSON(s, r, it, c))
				}
				return jsonResp(200, out)
			}
			if method == "POST" {
				text := stringField(parsed, "body")
				if text == "" {
					return errResp(422, "body is required")
				}
				c := s.addComment(it, text)
				return jsonResp(201, commentJSON(s, r, it, c))
			}
			return errResp(404, "Not Found")
		}
		return errResp(404, "Not Found")
	}

	if head == "pulls" {
		if len(tail) == 1 {
			if method == "GET" {
				page, perPage := pageParams(query)
				state := query.Get("state")
				if state == "" {
					state = "open"
				}
				all := r.listPulls(state)
				items, hasMore, totalPages := paginate(all, page, perPage)
				out := make([]any, 0, len(items))
				for _, p := range items {
					out = append(out, pullJSON(s, r, p))
				}
				res := jsonResp(200, out)
				if link := linkHeader(origin, path, page, perPage, hasMore, totalPages); link != "" {
					res.headers = map[string]string{"link": link}
				}
				return res
			}
			if method == "POST" {
				title := stringField(parsed, "title")
				headRef := lastSegment(stringField(parsed, "head"))
				baseRef := lastSegment(stringField(parsed, "base"))
				if title == "" || headRef == "" || baseRef == "" {
					return errResp(422, "title, head and base are required")
				}
				created, errMsg := s.createPull(r, title, stringField(parsed, "body"), headRef, baseRef, "", "")
				if errMsg != "" {
					return errResp(422, errMsg)
				}
				return jsonResp(201, pullJSON(s, r, created))
			}
			return errResp(404, "Not Found")
		}
		number, err := strconv.Atoi(tail[1])
		if err != nil {
			return errResp(404, "Not Found")
		}
		p := s.findPull(r, number)
		if len(tail) == 2 {
			if p == nil {
				return errResp(404, "Not Found")
			}
			if method == "GET" {
				return jsonResp(200, pullJSON(s, r, p))
			}
			return errResp(404, "Not Found")
		}
		if len(tail) == 3 && tail[2] == "files" {
			if p == nil {
				return errResp(404, "Not Found")
			}
			if method == "GET" {
				files := r.pullFiles(p)
				out := make([]any, 0, len(files))
				for _, f := range files {
					out = append(out, jsonMap{
						"filename": f.Filename, "status": f.Status,
						"additions": f.Additions, "deletions": f.Deletions,
						"changes": f.Additions + f.Deletions,
					})
				}
				return jsonResp(200, out)
			}
			return errResp(404, "Not Found")
		}
		if len(tail) == 3 && tail[2] == "merge" {
			if p == nil {
				return errResp(404, "Not Found")
			}
			if method != "PUT" {
				return errResp(404, "Not Found")
			}
			sha, ok, errMsg := s.mergePull(r, p)
			if !ok {
				if errMsg == "conflict" {
					return errResp(405, "Pull request is conflicted")
				}
				if errMsg == "closed" {
					return errResp(405, "Pull request is already closed")
				}
				return errResp(404, "Not Found")
			}
			return jsonResp(200, jsonMap{
				"sha": sha, "merged": true,
				"message": "Pull request #" + strconv.Itoa(p.Number) + " successfully merged",
			})
		}
		return errResp(404, "Not Found")
	}

	return errResp(404, "Not Found")
}

func dirEntries(s *store, r *repo, rootTree, dirPath string) []any {
	entries := r.listDir(rootTree, dirPath)
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		path := e.Name
		if dirPath != "" {
			path = dirPath + "/" + e.Name
		}
		size := 0
		typ := "file"
		if e.Mode == "40000" {
			typ = "dir"
		} else if obj := r.getObject(e.OID); obj != nil && obj.Type == "blob" {
			size = len(obj.Content)
		}
		out = append(out, jsonMap{
			"type": typ, "name": e.Name, "path": path, "sha": e.OID, "size": size,
			"url":          "/api/v3/repos/" + r.Owner + "/" + r.Name + "/contents/" + path,
			"html_url":     "",
			"git_url":      "",
			"download_url": "",
		})
	}
	return out
}

var _ = sort.Strings
