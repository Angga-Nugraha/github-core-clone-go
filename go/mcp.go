// MCP: JSON-RPC 2.0 Streamable HTTP on POST /mcp. Modern era
// (server/discover, revision 2026-07-28) and legacy handshake (initialize +
// notifications/initialized). Tools act on the same store as REST.
package main

import (
	"strconv"
	"strings"
)

const mcpServerName = "github-core-clone"
const mcpServerVersion = "1.0.0"

var mcpSupportedVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type mcpTool struct {
	name        string
	description string
	properties  []mcpProp
	required    []string
}

type mcpProp struct {
	name        string
	typ         string
	description string
}

var mcpTools = []mcpTool{
	{"create_repo", "Create a new repository for the authenticated user.", []mcpProp{
		{"name", "string", "Repository name"},
		{"description", "string", "Repository description"},
		{"private", "boolean", "Whether the repository is private"},
	}, []string{"name"}},
	{"get_repo", "Get a repository by owner and name.", []mcpProp{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"full_name", "string", "Full repository name owner/repo"},
	}, []string{"owner", "repo"}},
	{"create_issue", "Create an issue in a repository.", []mcpProp{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"title", "string", "Issue title"},
		{"body", "string", "Issue body"},
		{"full_name", "string", "Full repository name owner/repo"},
	}, []string{"owner", "repo", "title"}},
	{"list_issues", "List issues in a repository.", []mcpProp{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"state", "string", "Issue state: open, closed or all"},
		{"full_name", "string", "Full repository name owner/repo"},
	}, []string{"owner", "repo"}},
	{"get_issue", "Get one issue in a repository by number.", []mcpProp{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"issue_number", "number", "Issue number"},
		{"full_name", "string", "Full repository name owner/repo"},
	}, []string{"owner", "repo", "issue_number"}},
	{"create_pull_request", "Create a pull request in a repository.", []mcpProp{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"title", "string", "Pull request title"},
		{"head", "string", "Head branch name"},
		{"base", "string", "Base branch name"},
		{"body", "string", "Pull request body"},
		{"full_name", "string", "Full repository name owner/repo"},
	}, []string{"owner", "repo", "title", "head", "base"}},
	{"merge_pull_request", "Merge a pull request with a squash commit.", []mcpProp{
		{"owner", "string", "Repository owner login"},
		{"repo", "string", "Repository name"},
		{"pull_number", "number", "Pull request number"},
		{"full_name", "string", "Full repository name owner/repo"},
	}, []string{"owner", "repo", "pull_number"}},
}

// toolsListJSON is precomputed once so tools/list is byte-identical every call.
var toolsListJSON []byte

func init() {
	tools := make([]any, 0, len(mcpTools))
	for _, t := range mcpTools {
		props := jsonMap{}
		for _, p := range t.properties {
			props[p.name] = jsonMap{"type": p.typ, "description": p.description}
		}
		tools = append(tools, jsonMap{
			"name":        t.name,
			"description": t.description,
			"inputSchema": jsonMap{
				"type":                 "object",
				"properties":           props,
				"required":             t.required,
				"additionalProperties": true,
			},
		})
	}
	toolsListJSON = jsonBytes(jsonMap{"tools": tools})
}

func mcpRPCResult(id any, result any) []byte {
	if id == nil {
		id = nil
	}
	return append(jsonBytes(jsonMap{"jsonrpc": "2.0", "id": id, "result": result}), '\n')
}

func mcpRPCError(id any, code int, message string) []byte {
	return append(jsonBytes(jsonMap{"jsonrpc": "2.0", "id": id, "error": jsonMap{"code": code, "message": message}}), '\n')
}

func mcpToolResult(result any) jsonMap {
	text := ""
	if s, ok := result.(string); ok {
		text = s
	} else {
		text = string(jsonBytes(result))
	}
	return jsonMap{
		"content":           []any{jsonMap{"type": "text", "text": text}},
		"structuredContent": result,
		"isError":           false,
	}
}

func mcpToolError(message string) jsonMap {
	return jsonMap{
		"content":           []any{jsonMap{"type": "text", "text": message}},
		"structuredContent": jsonMap{"error": message},
		"isError":           true,
	}
}

var mcpSynonyms = map[string][]string{
	"owner":        {"owner", "owner_login", "ownerLogin", "login"},
	"repo":         {"repo", "repository", "repo_name", "repository_name", "name"},
	"title":        {"title"},
	"body":         {"body", "description"},
	"state":        {"state"},
	"head":         {"head", "head_ref", "headRef", "head_branch", "source_branch"},
	"base":         {"base", "base_ref", "baseRef", "base_branch", "target_branch"},
	"pull_number":  {"pull_number", "pullNumber", "number", "pr_number", "pull_request_number"},
	"issue_number": {"issue_number", "issueNumber", "number", "issue"},
	"name":         {"name", "repo", "repository"},
	"full_name":    {"full_name", "fullName", "repo_full_name"},
	"description":  {"description"},
	"private":      {"private"},
}

func mcpPickArg(args map[string]any, property string) any {
	candidates, ok := mcpSynonyms[property]
	if !ok {
		candidates = []string{property}
	}
	for _, c := range candidates {
		if v, ok := args[c]; ok && v != nil {
			return v
		}
	}
	return nil
}

func mcpResolveRepoArgs(args map[string]any) (owner, repo string) {
	ownerV := mcpPickArg(args, "owner")
	repoV := mcpPickArg(args, "repo")
	fullName := mcpPickArg(args, "full_name")
	owner, _ = ownerV.(string)
	repo, _ = repoV.(string)
	if owner == "" || repo == "" {
		if fn, ok := fullName.(string); ok {
			parts := strings.Split(fn, "/")
			if len(parts) == 2 {
				if owner == "" {
					owner = parts[0]
				}
				if repo == "" {
					repo = parts[1]
				}
			}
		}
	}
	if strings.Contains(repo, "/") {
		parts := strings.Split(repo, "/")
		if owner == "" {
			owner = parts[0]
		}
		repo = parts[1]
	}
	return owner, repo
}

func mcpMissingRequired(tool *mcpTool, args map[string]any) string {
	for _, property := range tool.required {
		if property == "owner" || property == "repo" {
			continue
		}
		if mcpPickArg(args, property) == nil {
			return property
		}
	}
	return ""
}

func mcpStringArg(args map[string]any, property string) string {
	if v := mcpPickArg(args, property); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	return ""
}

func mcpNumberArg(args map[string]any, property string) int {
	v := mcpPickArg(args, property)
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

// handleMcp handles one decoded JSON-RPC request.
// Returns (status, body); body nil means an empty response (202).
func handleMcp(s *store, body any) (int, []byte) {
	req, ok := body.(map[string]any)
	if !ok {
		return 400, mcpRPCError(nil, -32600, "Invalid Request")
	}
	method, _ := req["method"].(string)
	id, hasID := req["id"]
	if id == nil {
		hasID = false
	}
	if !hasID {
		id = nil
	}

	switch method {
	case "server/discover":
		return 200, mcpRPCResult(id, jsonMap{
			"supportedVersions": mcpSupportedVersions,
			"protocolVersion":   mcpSupportedVersions[0],
			"serverInfo":        jsonMap{"name": mcpServerName, "version": mcpServerVersion},
			"capabilities":      jsonMap{"tools": jsonMap{}},
			"_meta":             jsonMap{"io.modelcontextprotocol/serverInfo": jsonMap{"name": mcpServerName, "version": mcpServerVersion}},
		})
	case "initialize":
		params, _ := req["params"].(map[string]any)
		requested, _ := params["protocolVersion"].(string)
		version := mcpSupportedVersions[0]
		for _, v := range mcpSupportedVersions {
			if v == requested {
				version = v
				break
			}
		}
		return 200, mcpRPCResult(id, jsonMap{
			"protocolVersion": version,
			"capabilities":    jsonMap{"tools": jsonMap{}},
			"serverInfo":      jsonMap{"name": mcpServerName, "version": mcpServerVersion},
			"_meta":           jsonMap{"io.modelcontextprotocol/serverInfo": jsonMap{"name": mcpServerName, "version": mcpServerVersion}},
		})
	case "tools/list":
		// Byte-identical tools payload; id varies per request.
		out := append([]byte(`{"jsonrpc":"2.0","id":`), jsonBytes(id)...)
		out = append(out, []byte(`,"result":`)...)
		out = append(out, toolsListJSON...)
		out = append(out, "}\n"...)
		return 200, out
	case "tools/call":
		params, _ := req["params"].(map[string]any)
		rawName, _ := params["name"].(string)
		var tool *mcpTool
		for i := range mcpTools {
			if mcpTools[i].name == rawName {
				tool = &mcpTools[i]
				break
			}
		}
		if tool == nil {
			for i := range mcpTools {
				if strings.HasSuffix(strings.ToLower(rawName), mcpTools[i].name) {
					tool = &mcpTools[i]
					break
				}
			}
		}
		if tool == nil {
			return 200, mcpRPCError(id, -32602, "Unknown tool: "+rawName)
		}
		args, _ := params["arguments"].(map[string]any)
		if args == nil {
			args = map[string]any{}
		}
		if missing := mcpMissingRequired(tool, args); missing != "" {
			return 200, mcpRPCResult(id, mcpToolError("Missing required argument: "+missing))
		}
		owner, repoName := mcpResolveRepoArgs(args)
		needsOwner := false
		for _, r := range tool.required {
			if r == "owner" {
				needsOwner = true
			}
		}
		if needsOwner && (owner == "" || repoName == "") {
			return 200, mcpRPCResult(id, mcpToolError("Missing required argument: owner/repo"))
		}

		if tool.name == "create_repo" {
			name := mcpStringArg(args, "name")
			desc := mcpStringArg(args, "description")
			priv := mcpPickArg(args, "private") == true
			created, errMsg := s.createRepo(s.viewer.Login, name, desc, priv, "")
			if errMsg != "" {
				return 200, mcpRPCResult(id, mcpToolError(errMsg))
			}
			return 200, mcpRPCResult(id, mcpToolResult(jsonMap{
				"id": created.ID, "node_id": repoGlobalID(created),
				"name": created.Name, "full_name": created.FullName,
				"owner":   jsonMap{"login": created.Owner},
				"private": created.Private, "description": created.Description,
				"default_branch": created.DefaultBranch,
			}))
		}

		r := s.findRepo(owner, repoName)
		if r == nil {
			return 200, mcpRPCResult(id, mcpToolError("Repository "+owner+"/"+repoName+" not found"))
		}

		switch tool.name {
		case "get_repo":
			return 200, mcpRPCResult(id, mcpToolResult(jsonMap{
				"id": r.ID, "node_id": repoGlobalID(r),
				"name": r.Name, "full_name": r.FullName,
				"owner":   jsonMap{"login": r.Owner},
				"private": r.Private, "description": r.Description,
				"default_branch": r.DefaultBranch,
			}))
		case "create_issue":
			title := mcpStringArg(args, "title")
			bodyText := mcpStringArg(args, "body")
			created, errMsg := s.createIssue(r, title, bodyText, nil)
			if errMsg != "" {
				return 200, mcpRPCResult(id, mcpToolError(errMsg))
			}
			return 200, mcpRPCResult(id, mcpToolResult(jsonMap{
				"id": created.ID, "node_id": issueGlobalID(r, created),
				"number": created.Number, "title": created.Title, "body": created.Body,
				"state": created.State, "full_name": r.FullName, "repository": r.FullName,
			}))
		case "list_issues":
			state := mcpStringArg(args, "state")
			if state == "" {
				state = "open"
			}
			issues := r.listIssues(state, true)
			out := make([]any, 0, len(issues))
			for _, it := range issues {
				out = append(out, jsonMap{
					"id": it.ID, "node_id": issueGlobalID(r, it),
					"number": it.Number, "title": it.Title, "body": it.Body, "state": it.State,
				})
			}
			return 200, mcpRPCResult(id, mcpToolResult(out))
		case "get_issue":
			number := mcpNumberArg(args, "issue_number")
			it := s.findIssue(r, number)
			if it == nil {
				return 200, mcpRPCResult(id, mcpToolError("Issue #"+strconv.Itoa(number)+" not found"))
			}
			return 200, mcpRPCResult(id, mcpToolResult(jsonMap{
				"id": it.ID, "node_id": issueGlobalID(r, it),
				"number": it.Number, "title": it.Title, "body": it.Body, "state": it.State,
			}))
		case "create_pull_request":
			title := mcpStringArg(args, "title")
			head := mcpStringArg(args, "head")
			base := mcpStringArg(args, "base")
			bodyText := mcpStringArg(args, "body")
			created, errMsg := s.createPull(r, title, bodyText, head, base, "", "")
			if errMsg != "" {
				return 200, mcpRPCResult(id, mcpToolError(errMsg))
			}
			return 200, mcpRPCResult(id, mcpToolResult(jsonMap{
				"id": created.ID, "node_id": pullGlobalID(r, created),
				"number": created.Number, "title": created.Title, "body": created.Body,
				"state":     created.State,
				"head":      jsonMap{"ref": created.Head, "sha": created.HeadSHA},
				"base":      jsonMap{"ref": created.Base, "sha": created.BaseSHA},
				"full_name": r.FullName,
			}))
		case "merge_pull_request":
			number := mcpNumberArg(args, "pull_number")
			p := s.findPull(r, number)
			if p == nil {
				return 200, mcpRPCResult(id, mcpToolError("Pull request #"+strconv.Itoa(number)+" not found"))
			}
			sha, ok, errMsg := s.mergePull(r, p)
			if !ok {
				msg := "Pull request cannot be merged"
				if errMsg == "conflict" {
					msg = "Pull request is conflicted"
				}
				return 200, mcpRPCResult(id, mcpToolError(msg))
			}
			return 200, mcpRPCResult(id, mcpToolResult(jsonMap{
				"sha": sha, "merged": true, "number": p.Number, "state": p.State,
			}))
		}
	}

	if strings.HasPrefix(method, "notifications/") {
		return 202, nil
	}
	if !hasID {
		return 202, nil
	}
	return 200, mcpRPCError(id, -32601, "Method not found: "+method)
}
