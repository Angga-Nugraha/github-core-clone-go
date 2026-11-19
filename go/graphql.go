// Minimal GraphQL executor: tokenizer + recursive-descent parser + resolver
// for exactly the surface the suite exercises. Unknown fields produce an
// errors array naming the field, with HTTP 200.
package main

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// ---------- tokenizer ----------

type gtoken struct {
	kind  string // name | string | number | punct | spread | eof
	value string
}

func gtokenize(source string) []gtoken {
	tokens := []gtoken{}
	i := 0
	for i < len(source) {
		ch := source[i]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == ',' || ch == 0xEF {
			// note: FEFF is 3 bytes in UTF-8; handled below by the catch-all skip
			i++
			continue
		}
		if ch == '#' {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if ch == '.' && i+2 < len(source) && source[i+1] == '.' && source[i+2] == '.' {
			tokens = append(tokens, gtoken{kind: "spread"})
			i += 3
			continue
		}
		if strings.IndexByte("{}()[]:!=@$&|", ch) >= 0 {
			tokens = append(tokens, gtoken{kind: "punct", value: string(ch)})
			i++
			continue
		}
		if ch == '"' {
			var value strings.Builder
			i++
			for i < len(source) && source[i] != '"' {
				if source[i] == '\\' && i+1 < len(source) {
					esc := source[i+1]
					switch esc {
					case 'n':
						value.WriteByte('\n')
					case 't':
						value.WriteByte('\t')
					case 'r':
						value.WriteByte('\r')
					case 'u':
						if i+5 < len(source) {
							if n, err := strconv.ParseInt(source[i+2:i+6], 16, 32); err == nil {
								value.WriteRune(rune(n))
							}
							i += 4
						}
					default:
						value.WriteByte(esc)
					}
					i += 2
				} else {
					value.WriteByte(source[i])
					i++
				}
			}
			i++
			tokens = append(tokens, gtoken{kind: "string", value: value.String()})
			continue
		}
		if ch >= '0' && ch <= '9' || ch == '-' {
			j := i
			for j < len(source) && (source[j] >= '0' && source[j] <= '9' || source[j] == '.' || source[j] == 'e' || source[j] == 'E' || source[j] == '+' || source[j] == '-') {
				j++
			}
			tokens = append(tokens, gtoken{kind: "number", value: source[i:j]})
			i = j
			continue
		}
		if ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' {
			j := i
			for j < len(source) && (source[j] == '_' || source[j] >= 'A' && source[j] <= 'Z' || source[j] >= 'a' && source[j] <= 'z' || source[j] >= '0' && source[j] <= '9') {
				j++
			}
			tokens = append(tokens, gtoken{kind: "name", value: source[i:j]})
			i = j
			continue
		}
		i++
	}
	tokens = append(tokens, gtoken{kind: "eof"})
	return tokens
}

// ---------- AST ----------

type gvalue struct {
	kind   string // variable | string | int | float | boolean | null | enum | list | object
	str    string
	num    float64
	b      bool
	name   string
	values []*gvalue
	fields []gfieldArg
}

type gfieldArg struct {
	name  string
	value *gvalue
}

type gselection struct {
	kind          string // field | spread | inline
	name          string
	alias         string
	args          []gfieldArg
	selections    []*gselection
	typeCondition string
}

type goperation struct {
	operation  string // query | mutation | subscription
	name       string
	varDefs    []gvarDef
	selections []*gselection
}

type gvarDef struct {
	name         string
	defaultValue *gvalue
}

type gfragment struct {
	name          string
	typeCondition string
	selections    []*gselection
}

type gdocument struct {
	operations []*goperation
	fragments  map[string]*gfragment
}

type gparser struct {
	tokens []gtoken
	pos    int
}

func (p *gparser) peek() gtoken { return p.tokens[p.pos] }
func (p *gparser) next() gtoken { t := p.tokens[p.pos]; p.pos++; return t }
func (p *gparser) isPunct(v string) bool {
	t := p.peek()
	return t.kind == "punct" && t.value == v
}

func (p *gparser) expectPunct(v string) error {
	t := p.next()
	if t.kind != "punct" || t.value != v {
		return fmt.Errorf("expected %s", v)
	}
	return nil
}

func (p *gparser) expectName(want ...string) (string, error) {
	t := p.next()
	if t.kind != "name" {
		return "", fmt.Errorf("expected name")
	}
	if len(want) > 0 && t.value != want[0] {
		return "", fmt.Errorf("expected %s", want[0])
	}
	return t.value, nil
}

func (p *gparser) parseDocument() (*gdocument, error) {
	doc := &gdocument{fragments: map[string]*gfragment{}}
	for p.peek().kind != "eof" {
		if p.isPunct("{") {
			sel, err := p.parseSelectionSet()
			if err != nil {
				return nil, err
			}
			doc.operations = append(doc.operations, &goperation{operation: "query", selections: sel})
		} else {
			keyword, err := p.expectName()
			if err != nil {
				return nil, err
			}
			if keyword == "fragment" {
				name, err := p.expectName()
				if err != nil {
					return nil, err
				}
				if _, err := p.expectName("on"); err != nil {
					return nil, err
				}
				tc, err := p.expectName()
				if err != nil {
					return nil, err
				}
				sel, err := p.parseSelectionSet()
				if err != nil {
					return nil, err
				}
				doc.fragments[name] = &gfragment{name: name, typeCondition: tc, selections: sel}
			} else if keyword == "query" || keyword == "mutation" || keyword == "subscription" {
				var name string
				if p.peek().kind == "name" {
					name = p.next().value
				}
				var defs []gvarDef
				if p.isPunct("(") {
					defs, err = p.parseVariableDefinitions()
					if err != nil {
						return nil, err
					}
				}
				sel, err := p.parseSelectionSet()
				if err != nil {
					return nil, err
				}
				doc.operations = append(doc.operations, &goperation{operation: keyword, name: name, varDefs: defs, selections: sel})
			} else {
				return nil, fmt.Errorf("unexpected keyword %s", keyword)
			}
		}
	}
	return doc, nil
}

func (p *gparser) parseVariableDefinitions() ([]gvarDef, error) {
	var defs []gvarDef
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	for !p.isPunct(")") {
		if err := p.expectPunct("$"); err != nil {
			return nil, err
		}
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(":"); err != nil {
			return nil, err
		}
		if err := p.parseType(); err != nil {
			return nil, err
		}
		var def *gvalue
		if p.isPunct("=") {
			p.next()
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			def = v
		}
		defs = append(defs, gvarDef{name: name, defaultValue: def})
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	return defs, nil
}

func (p *gparser) parseType() error {
	if p.isPunct("[") {
		p.next()
		if err := p.parseType(); err != nil {
			return err
		}
		if err := p.expectPunct("]"); err != nil {
			return err
		}
	} else {
		if _, err := p.expectName(); err != nil {
			return err
		}
	}
	if p.isPunct("!") {
		p.next()
	}
	return nil
}

func (p *gparser) parseSelectionSet() ([]*gselection, error) {
	var sels []*gselection
	if err := p.expectPunct("{"); err != nil {
		return nil, err
	}
	for !p.isPunct("}") {
		sel, err := p.parseSelection()
		if err != nil {
			return nil, err
		}
		sels = append(sels, sel)
	}
	if err := p.expectPunct("}"); err != nil {
		return nil, err
	}
	return sels, nil
}

func (p *gparser) parseSelection() (*gselection, error) {
	if p.peek().kind == "spread" {
		p.next()
		if p.peek().kind == "name" && p.peek().value == "on" {
			p.next()
			tc, err := p.expectName()
			if err != nil {
				return nil, err
			}
			sel, err := p.parseSelectionSet()
			if err != nil {
				return nil, err
			}
			return &gselection{kind: "inline", typeCondition: tc, selections: sel}, nil
		}
		if p.peek().kind == "name" {
			return &gselection{kind: "spread", name: p.next().value}, nil
		}
		sel, err := p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
		return &gselection{kind: "inline", selections: sel}, nil
	}
	first, err := p.expectName()
	if err != nil {
		return nil, err
	}
	alias, name := "", first
	if p.isPunct(":") {
		p.next()
		alias = first
		name, err = p.expectName()
		if err != nil {
			return nil, err
		}
	}
	var args []gfieldArg
	if p.isPunct("(") {
		args, err = p.parseArguments()
		if err != nil {
			return nil, err
		}
	}
	for p.isPunct("@") {
		p.next()
		if _, err := p.expectName(); err != nil {
			return nil, err
		}
		if p.isPunct("(") {
			if _, err := p.parseArguments(); err != nil {
				return nil, err
			}
		}
	}
	var sel []*gselection
	if p.isPunct("{") {
		sel, err = p.parseSelectionSet()
		if err != nil {
			return nil, err
		}
	}
	return &gselection{kind: "field", name: name, alias: alias, args: args, selections: sel}, nil
}

func (p *gparser) parseArguments() ([]gfieldArg, error) {
	var args []gfieldArg
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	for !p.isPunct(")") {
		name, err := p.expectName()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(":"); err != nil {
			return nil, err
		}
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		args = append(args, gfieldArg{name: name, value: v})
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	return args, nil
}

func (p *gparser) parseValue() (*gvalue, error) {
	t := p.next()
	switch t.kind {
	case "string":
		return &gvalue{kind: "string", str: t.value}, nil
	case "number":
		n, _ := strconv.ParseFloat(t.value, 64)
		if strings.ContainsAny(t.value, ".eE") {
			return &gvalue{kind: "float", num: n}, nil
		}
		return &gvalue{kind: "int", num: n}, nil
	case "punct":
		switch t.value {
		case "$":
			name, err := p.expectName()
			if err != nil {
				return nil, err
			}
			return &gvalue{kind: "variable", name: name}, nil
		case "[":
			var values []*gvalue
			for !(p.peek().kind == "punct" && p.peek().value == "]") {
				v, err := p.parseValue()
				if err != nil {
					return nil, err
				}
				values = append(values, v)
			}
			if err := p.expectPunct("]"); err != nil {
				return nil, err
			}
			return &gvalue{kind: "list", values: values}, nil
		case "{":
			var fields []gfieldArg
			for !(p.peek().kind == "punct" && p.peek().value == "}") {
				name, err := p.expectName()
				if err != nil {
					return nil, err
				}
				if err := p.expectPunct(":"); err != nil {
					return nil, err
				}
				v, err := p.parseValue()
				if err != nil {
					return nil, err
				}
				fields = append(fields, gfieldArg{name: name, value: v})
			}
			if err := p.expectPunct("}"); err != nil {
				return nil, err
			}
			return &gvalue{kind: "object", fields: fields}, nil
		}
	case "name":
		switch t.value {
		case "true":
			return &gvalue{kind: "boolean", b: true}, nil
		case "false":
			return &gvalue{kind: "boolean", b: false}, nil
		case "null":
			return &gvalue{kind: "null"}, nil
		default:
			return &gvalue{kind: "enum", str: t.value}, nil
		}
	}
	return nil, fmt.Errorf("unexpected value")
}

func gvalueFromNode(node *gvalue, vars map[string]any) any {
	switch node.kind {
	case "variable":
		if v, ok := vars[node.name]; ok {
			return v
		}
		return nil
	case "string":
		return node.str
	case "int", "float":
		return node.num
	case "boolean":
		return node.b
	case "null":
		return nil
	case "enum":
		return node.str
	case "list":
		out := make([]any, 0, len(node.values))
		for _, c := range node.values {
			out = append(out, gvalueFromNode(c, vars))
		}
		return out
	case "object":
		out := map[string]any{}
		for _, f := range node.fields {
			out[f.name] = gvalueFromNode(f.value, vars)
		}
		return out
	}
	return nil
}

// ---------- nodes ----------

type gfile struct {
	path       string
	additions  int
	deletions  int
	changeType string
}

type gnode struct {
	kind        string
	op          string
	repo        *repo
	issue       *issue
	pull        *pull
	issues      []*issue
	pulls       []*pull
	files       []gfile
	hasNext     bool
	endCursor   *string
	name        string
	color       string
	commentID   int
	commentBody string
	edgeNode    *gnode
	cursor      string
	typeName    string
	typeKind    string
	typeFields  []string
	enumValues  []string
	values      map[string]any
}

func gtypeNameOf(n *gnode) string {
	switch n.kind {
	case "Root":
		if n.op == "mutation" {
			return "Mutation"
		}
		return "Query"
	case "User":
		return "User"
	case "Repository":
		return "Repository"
	case "Issue":
		return "Issue"
	case "PullRequest":
		return "PullRequest"
	case "IssueConnection":
		return "IssueConnection"
	case "PullRequestConnection":
		return "PullRequestConnection"
	case "FileConnection":
		return "FileChangeConnection"
	case "PageInfo":
		return "PageInfo"
	case "Label":
		return "Label"
	case "Comment":
		return "IssueComment"
	case "Edge":
		if n.edgeNode != nil && n.edgeNode.kind == "Issue" {
			return "IssueEdge"
		}
		if n.edgeNode != nil && n.edgeNode.kind == "PullRequest" {
			return "PullRequestEdge"
		}
		return "Edge"
	case "Type":
		return "__Type"
	case "Schema":
		return "__Schema"
	case "Payload":
		return "Payload"
	}
	return ""
}

var gtypeFields = map[string][]string{
	"Issue":       {"id", "number", "title", "body", "state", "url", "createdAt", "updatedAt", "closedAt", "author", "labels", "comments"},
	"PullRequest": {"id", "number", "title", "body", "state", "merged", "mergeable", "headRefName", "baseRefName", "headRefOid", "baseRefOid", "files", "author"},
	"Repository":  {"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequest", "pullRequests"},
	"User":        {"id", "databaseId", "login", "name", "email"},
	"SearchType":  {},
}

var genumValues = map[string][]string{
	"SearchType":       {"ISSUE", "REPOSITORY", "USER", "CODE", "DISCUSSION"},
	"IssueState":       {"OPEN", "CLOSED"},
	"PullRequestState": {"OPEN", "CLOSED", "MERGED"},
	"MergeableState":   {"MERGEABLE", "CONFLICTING", "UNKNOWN"},
}

// ---------- executor ----------

type gctx struct {
	store     *store
	variables map[string]any
	doc       *gdocument
	errors    []jsonMap
}

func gargValue(f *gselection, name string, ctx *gctx) any {
	for _, a := range f.args {
		if a.name == name {
			return gvalueFromNode(a.value, ctx.variables)
		}
	}
	return nil
}

func gstringArg(f *gselection, name string, ctx *gctx) (string, bool) {
	v := gargValue(f, name, ctx)
	s, ok := v.(string)
	return s, ok
}

func gintArg(f *gselection, name string, ctx *gctx) (int, bool) {
	v := gargValue(f, name, ctx)
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func cursorForSeq(seq int) string { return b64url("c:" + strconv.Itoa(seq)) }

func seqFromCursor(cursor *string) (int, bool) {
	if cursor == nil {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(*cursor)
	if err != nil {
		return 0, false
	}
	text := string(raw)
	if !strings.HasPrefix(text, "c:") {
		return 0, false
	}
	seq, err := strconv.Atoi(text[2:])
	if err != nil {
		return 0, false
	}
	return seq, true
}

func paginateSeqIssues(items []*issue, first *int, after *string, desc bool) (page []*issue, hasNext bool, endCursor *string) {
	sorted := append([]*issue{}, items...)
	if desc {
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	start := 0
	if seq, ok := seqFromCursor(after); ok {
		for i, it := range sorted {
			if it.Seq == seq {
				start = i + 1
				break
			}
		}
	}
	count := len(sorted)
	if first != nil {
		count = *first
	}
	end := start + count
	if end > len(sorted) {
		end = len(sorted)
	}
	if start > len(sorted) {
		start = len(sorted)
	}
	page = sorted[start:end]
	hasNext = start+len(page) < len(sorted)
	if len(page) > 0 {
		c := cursorForSeq(page[len(page)-1].Seq)
		endCursor = &c
	}
	return page, hasNext, endCursor
}

func paginateSeqPulls(items []*pull, first *int, after *string, desc bool) (page []*pull, hasNext bool, endCursor *string) {
	sorted := append([]*pull{}, items...)
	if desc {
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	start := 0
	if seq, ok := seqFromCursor(after); ok {
		for i, p := range sorted {
			if p.Seq == seq {
				start = i + 1
				break
			}
		}
	}
	count := len(sorted)
	if first != nil {
		count = *first
	}
	end := start + count
	if end > len(sorted) {
		end = len(sorted)
	}
	if start > len(sorted) {
		start = len(sorted)
	}
	page = sorted[start:end]
	hasNext = start+len(page) < len(sorted)
	if len(page) > 0 {
		c := cursorForSeq(page[len(page)-1].Seq)
		endCursor = &c
	}
	return page, hasNext, endCursor
}

// decodeGlobalID mirrors Node's store.decodeGlobalId.
func decodeGlobalID(id string) (kind string, repoID, number, commentID int, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", 0, 0, 0, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) < 2 {
		return "", 0, 0, 0, false
	}
	switch parts[0] {
	case "R":
		if len(parts) != 2 {
			return "", 0, 0, 0, false
		}
		repoID, err = strconv.Atoi(parts[1])
		return "R", repoID, 0, 0, err == nil
	case "I":
		if len(parts) != 3 {
			return "", 0, 0, 0, false
		}
		repoID, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, 0, false
		}
		number, err = strconv.Atoi(parts[2])
		return "I", repoID, number, 0, err == nil
	case "P":
		if len(parts) != 3 {
			return "", 0, 0, 0, false
		}
		repoID, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, 0, false
		}
		number, err = strconv.Atoi(parts[2])
		return "P", repoID, number, 0, err == nil
	case "C":
		if len(parts) != 4 {
			return "", 0, 0, 0, false
		}
		repoID, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, 0, false
		}
		number, err = strconv.Atoi(parts[2])
		if err != nil {
			return "", 0, 0, 0, false
		}
		commentID, err = strconv.Atoi(parts[3])
		return "C", repoID, number, commentID, err == nil
	}
	return "", 0, 0, 0, false
}

// resolveField returns (value, defined). defined=false means "undefined"
// (unknown field -> error); a nil value with defined=true means JSON null.
func resolveField(s *store, node *gnode, field *gselection, ctx *gctx) (any, bool) {
	switch node.kind {
	case "Root":
		if node.op == "query" {
			switch field.name {
			case "viewer":
				return &gnode{kind: "User"}, true
			case "repository":
				owner, ok1 := gstringArg(field, "owner", ctx)
				name, ok2 := gstringArg(field, "name", ctx)
				if !ok1 || !ok2 {
					return nil, true
				}
				r := s.findRepo(owner, name)
				if r == nil {
					return nil, true
				}
				return &gnode{kind: "Repository", repo: r}, true
			case "node":
				id, ok := gstringArg(field, "id", ctx)
				if !ok {
					return nil, true
				}
				kind, repoID, number, _, ok := decodeGlobalID(id)
				if !ok {
					return nil, true
				}
				r := s.findRepoByID(repoID)
				if r == nil {
					return nil, true
				}
				switch kind {
				case "R":
					return &gnode{kind: "Repository", repo: r}, true
				case "I":
					it := s.findIssue(r, number)
					if it == nil {
						return nil, true
					}
					return &gnode{kind: "Issue", repo: r, issue: it}, true
				case "P":
					p := s.findPull(r, number)
					if p == nil {
						return nil, true
					}
					return &gnode{kind: "PullRequest", repo: r, pull: p}, true
				}
				return nil, true
			case "__type":
				name, ok := gstringArg(field, "name", ctx)
				if !ok {
					return nil, true
				}
				if vals, ok := genumValues[name]; ok {
					return &gnode{kind: "Type", typeName: name, typeKind: "ENUM", enumValues: vals}, true
				}
				if fields, ok := gtypeFields[name]; ok {
					return &gnode{kind: "Type", typeName: name, typeKind: "OBJECT", typeFields: fields}, true
				}
				return nil, true
			case "__schema":
				return &gnode{kind: "Schema"}, true
			}
		} else {
			// Mutations.
			switch field.name {
			case "createIssue":
				input, _ := gargValue(field, "input", ctx).(map[string]any)
				var r *repo
				if rid, ok := input["repositoryId"].(string); ok {
					if _, repoID, _, _, ok := decodeGlobalID(rid); ok {
						r = s.findRepoByID(repoID)
					}
				}
				if r == nil {
					ctx.errors = append(ctx.errors, jsonMap{"message": "repository not found"})
					return nil, true
				}
				title, _ := input["title"].(string)
				body, _ := input["body"].(string)
				created, errMsg := s.createIssue(r, title, body, nil)
				if errMsg != "" {
					ctx.errors = append(ctx.errors, jsonMap{"message": errMsg})
					return nil, true
				}
				return &gnode{kind: "Payload", values: map[string]any{
					"issue":            &gnode{kind: "Issue", repo: r, issue: created},
					"clientMutationId": input["clientMutationId"],
				}}, true
			case "closeIssue":
				input, _ := gargValue(field, "input", ctx).(map[string]any)
				var r *repo
				var it *issue
				if iid, ok := input["issueId"].(string); ok {
					if _, repoID, number, _, ok := decodeGlobalID(iid); ok {
						r = s.findRepoByID(repoID)
						if r != nil {
							it = s.findIssue(r, number)
						}
					}
				}
				if it == nil {
					ctx.errors = append(ctx.errors, jsonMap{"message": "issue not found"})
					return nil, true
				}
				it.State = "closed"
				it.ClosedAt = s.nowISO()
				it.UpdatedAt = it.ClosedAt
				it.jsonCache = nil
				return &gnode{kind: "Payload", values: map[string]any{
					"issue": &gnode{kind: "Issue", repo: r, issue: it},
				}}, true
			case "createPullRequest":
				input, _ := gargValue(field, "input", ctx).(map[string]any)
				var r *repo
				if rid, ok := input["repositoryId"].(string); ok {
					if _, repoID, _, _, ok := decodeGlobalID(rid); ok {
						r = s.findRepoByID(repoID)
					}
				}
				if r == nil {
					ctx.errors = append(ctx.errors, jsonMap{"message": "repository not found"})
					return nil, true
				}
				head, _ := input["headRefName"].(string)
				base, _ := input["baseRefName"].(string)
				title, _ := input["title"].(string)
				body, _ := input["body"].(string)
				created, errMsg := s.createPull(r, title, body, head, base, "", "")
				if errMsg != "" {
					ctx.errors = append(ctx.errors, jsonMap{"message": errMsg})
					return nil, true
				}
				return &gnode{kind: "Payload", values: map[string]any{
					"pullRequest": &gnode{kind: "PullRequest", repo: r, pull: created},
				}}, true
			case "mergePullRequest":
				input, _ := gargValue(field, "input", ctx).(map[string]any)
				var r *repo
				var p *pull
				if pid, ok := input["pullRequestId"].(string); ok {
					if _, repoID, number, _, ok := decodeGlobalID(pid); ok {
						r = s.findRepoByID(repoID)
						if r != nil {
							p = s.findPull(r, number)
						}
					}
				}
				if p == nil {
					ctx.errors = append(ctx.errors, jsonMap{"message": "pull request not found"})
					return nil, true
				}
				_, ok, errMsg := s.mergePull(r, p)
				if !ok {
					msg := "Pull request cannot be merged"
					if errMsg == "conflict" {
						msg = "Pull request is conflicted"
					}
					ctx.errors = append(ctx.errors, jsonMap{"message": msg})
					return nil, true
				}
				return &gnode{kind: "Payload", values: map[string]any{
					"pullRequest": &gnode{kind: "PullRequest", repo: r, pull: p},
				}}, true
			case "addComment":
				input, _ := gargValue(field, "input", ctx).(map[string]any)
				var r *repo
				var it *issue
				if sid, ok := input["subjectId"].(string); ok {
					if _, repoID, number, _, ok := decodeGlobalID(sid); ok {
						r = s.findRepoByID(repoID)
						if r != nil {
							it = s.findIssue(r, number)
						}
					}
				}
				if it == nil {
					ctx.errors = append(ctx.errors, jsonMap{"message": "subject not found"})
					return nil, true
				}
				body, _ := input["body"].(string)
				c := s.addComment(it, body)
				return &gnode{kind: "Payload", values: map[string]any{
					"commentEdge": &gnode{kind: "Payload", values: map[string]any{
						"node":   &gnode{kind: "Comment", repo: r, issue: it, commentID: c.ID, commentBody: c.Body},
						"cursor": cursorForSeq(it.Seq),
					}},
				}}, true
			}
		}
		return nil, false
	case "User":
		v := s.viewer
		switch field.name {
		case "login":
			return v.Login, true
		case "name":
			return v.Name, true
		case "email":
			return v.Email, true
		case "id":
			return strconv.Itoa(v.ID), true
		case "databaseId":
			return v.ID, true
		}
		return nil, false
	case "Repository":
		r := node.repo
		switch field.name {
		case "id":
			return repoGlobalID(r), true
		case "databaseId":
			return r.ID, true
		case "name":
			return r.Name, true
		case "nameWithOwner":
			return r.FullName, true
		case "owner":
			return &gnode{kind: "User"}, true
		case "defaultBranchRef":
			tip := r.defaultTip()
			if tip == "" {
				return nil, true
			}
			return &gnode{kind: "Payload", values: map[string]any{"name": r.DefaultBranch, "target": nil}}, true
		case "viewerPermission":
			return "ADMIN", true
		case "hasIssuesEnabled":
			return true, true
		case "issue":
			number, ok := gintArg(field, "number", ctx)
			if !ok {
				return nil, true
			}
			it := s.findIssue(r, number)
			if it == nil {
				return nil, true
			}
			return &gnode{kind: "Issue", repo: r, issue: it}, true
		case "issues":
			var wanted map[string]bool
			if states, ok := gargValue(field, "states", ctx).([]any); ok {
				wanted = map[string]bool{}
				for _, st := range states {
					if str, ok := st.(string); ok {
						wanted[strings.ToLower(str)] = true
					}
				}
			}
			items := make([]*issue, 0, len(r.Issues))
			for _, it := range r.Issues {
				if wanted != nil && !wanted[it.State] {
					continue
				}
				items = append(items, it)
			}
			desc := false
			if ob, ok := gargValue(field, "orderBy", ctx).(map[string]any); ok {
				desc = ob["direction"] == "DESC"
			}
			var first *int
			if f, ok := gintArg(field, "first", ctx); ok {
				first = &f
			}
			var after *string
			if a, ok := gstringArg(field, "after", ctx); ok {
				after = &a
			}
			page, hasNext, endCursor := paginateSeqIssues(items, first, after, desc)
			return &gnode{kind: "IssueConnection", repo: r, issues: page, hasNext: hasNext, endCursor: endCursor}, true
		case "pullRequest":
			number, ok := gintArg(field, "number", ctx)
			if !ok {
				return nil, true
			}
			p := s.findPull(r, number)
			if p == nil {
				return nil, true
			}
			return &gnode{kind: "PullRequest", repo: r, pull: p}, true
		case "pullRequests":
			var wanted map[string]bool
			hasStates := false
			if states, ok := gargValue(field, "states", ctx).([]any); ok {
				hasStates = true
				wanted = map[string]bool{}
				for _, st := range states {
					if str, ok := st.(string); ok {
						t := strings.ToLower(str)
						if t == "merged" {
							t = "closed"
						}
						wanted[t] = true
					}
				}
			}
			items := make([]*pull, 0, len(r.Pulls))
			for _, p := range r.Pulls {
				if hasStates {
					st := p.State
					if p.Merged {
						st = "merged"
					}
					if !wanted[st] {
						continue
					}
				} else if p.State != "open" {
					continue
				}
				items = append(items, p)
			}
			desc := false
			if ob, ok := gargValue(field, "orderBy", ctx).(map[string]any); ok {
				desc = ob["direction"] == "DESC"
			}
			var first *int
			if f, ok := gintArg(field, "first", ctx); ok {
				first = &f
			}
			var after *string
			if a, ok := gstringArg(field, "after", ctx); ok {
				after = &a
			}
			page, hasNext, endCursor := paginateSeqPulls(items, first, after, desc)
			return &gnode{kind: "PullRequestConnection", repo: r, pulls: page, hasNext: hasNext, endCursor: endCursor}, true
		}
		return nil, false
	case "Issue":
		it := node.issue
		switch field.name {
		case "id":
			return issueGlobalID(node.repo, it), true
		case "number":
			return it.Number, true
		case "title":
			return it.Title, true
		case "body":
			return it.Body, true
		case "state":
			return strings.ToUpper(it.State), true
		case "url":
			return "https://example.invalid/" + node.repo.FullName + "/issues/" + strconv.Itoa(it.Number), true
		case "createdAt":
			return it.CreatedAt, true
		case "updatedAt":
			return it.UpdatedAt, true
		case "closedAt":
			if it.ClosedAt == "" {
				return nil, true
			}
			return it.ClosedAt, true
		case "author":
			return &gnode{kind: "User"}, true
		case "labels":
			nodes := []any{}
			for _, name := range it.Labels {
				color := "ededed"
				if l, ok := node.repo.Labels[name]; ok {
					color = l.Color
				}
				nodes = append(nodes, &gnode{kind: "Label", name: name, color: color})
			}
			return &gnode{kind: "Payload", values: map[string]any{"nodes": nodes}}, true
		case "comments":
			nodes := []any{}
			for _, c := range it.Comments {
				nodes = append(nodes, &gnode{kind: "Comment", repo: node.repo, issue: it, commentID: c.ID, commentBody: c.Body})
			}
			return &gnode{kind: "Payload", values: map[string]any{"nodes": nodes}}, true
		}
		return nil, false
	case "PullRequest":
		p := node.pull
		switch field.name {
		case "id":
			return pullGlobalID(node.repo, p), true
		case "number":
			return p.Number, true
		case "title":
			return p.Title, true
		case "body":
			return p.Body, true
		case "state":
			if p.Merged {
				return "MERGED", true
			}
			return strings.ToUpper(p.State), true
		case "merged":
			return p.Merged, true
		case "mergeable":
			if p.State != "open" {
				return "UNKNOWN", true
			}
			headTip := node.repo.Refs["refs/heads/"+p.Head]
			baseTip := node.repo.Refs["refs/heads/"+p.Base]
			if headTip == "" || baseTip == "" {
				return "UNKNOWN", true
			}
			if s.prConflict(node.repo, p) {
				return "CONFLICTING", true
			}
			return "MERGEABLE", true
		case "headRefName":
			return p.Head, true
		case "baseRefName":
			return p.Base, true
		case "headRefOid":
			return p.HeadSHA, true
		case "baseRefOid":
			return p.BaseSHA, true
		case "author":
			return &gnode{kind: "User"}, true
		case "files":
			changed := node.repo.pullFiles(p)
			files := make([]gfile, 0, len(changed))
			for _, f := range changed {
				ct := "MODIFIED"
				if f.Status == "added" {
					ct = "ADDED"
				} else if f.Status == "removed" {
					ct = "REMOVED"
				}
				files = append(files, gfile{path: f.Filename, additions: f.Additions, deletions: f.Deletions, changeType: ct})
			}
			if first, ok := gintArg(field, "first", ctx); ok && first < len(files) {
				files = files[:first]
			}
			return &gnode{kind: "FileConnection", repo: node.repo, pull: p, files: files}, true
		}
		return nil, false
	case "IssueConnection":
		switch field.name {
		case "nodes":
			out := []any{}
			for _, it := range node.issues {
				out = append(out, &gnode{kind: "Issue", repo: node.repo, issue: it})
			}
			return out, true
		case "edges":
			out := []any{}
			for _, it := range node.issues {
				out = append(out, &gnode{kind: "Edge", edgeNode: &gnode{kind: "Issue", repo: node.repo, issue: it}, cursor: cursorForSeq(it.Seq)})
			}
			return out, true
		case "pageInfo":
			return &gnode{kind: "PageInfo", hasNext: node.hasNext, endCursor: node.endCursor}, true
		case "totalCount":
			return len(node.repo.Issues), true
		}
		return nil, false
	case "PullRequestConnection":
		switch field.name {
		case "nodes":
			out := []any{}
			for _, p := range node.pulls {
				out = append(out, &gnode{kind: "PullRequest", repo: node.repo, pull: p})
			}
			return out, true
		case "edges":
			out := []any{}
			for _, p := range node.pulls {
				out = append(out, &gnode{kind: "Edge", edgeNode: &gnode{kind: "PullRequest", repo: node.repo, pull: p}, cursor: cursorForSeq(p.Seq)})
			}
			return out, true
		case "pageInfo":
			return &gnode{kind: "PageInfo", hasNext: node.hasNext, endCursor: node.endCursor}, true
		case "totalCount":
			return len(node.repo.Pulls), true
		}
		return nil, false
	case "FileConnection":
		switch field.name {
		case "nodes":
			out := []any{}
			for _, f := range node.files {
				out = append(out, &gnode{kind: "Payload", values: map[string]any{
					"path": f.path, "additions": f.additions, "deletions": f.deletions, "changeType": f.changeType,
				}})
			}
			return out, true
		case "pageInfo":
			return &gnode{kind: "PageInfo", hasNext: false, endCursor: nil}, true
		case "totalCount":
			return len(node.files), true
		}
		return nil, false
	case "PageInfo":
		switch field.name {
		case "hasNextPage":
			return node.hasNext, true
		case "hasPreviousPage":
			return false, true
		case "startCursor":
			return node.endCursor, true
		case "endCursor":
			return node.endCursor, true
		}
		return nil, false
	case "Label":
		switch field.name {
		case "name":
			return node.name, true
		case "color":
			return node.color, true
		case "id":
			return b64url("L:" + node.name), true
		}
		return nil, false
	case "Comment":
		switch field.name {
		case "id":
			return gid("C", strconv.Itoa(node.repo.ID), strconv.Itoa(node.issue.Number), strconv.Itoa(node.commentID)), true
		case "body":
			return node.commentBody, true
		case "author":
			return &gnode{kind: "User"}, true
		}
		return nil, false
	case "Edge":
		switch field.name {
		case "node":
			return node.edgeNode, true
		case "cursor":
			return node.cursor, true
		}
		return nil, false
	case "Type":
		switch field.name {
		case "name":
			return node.typeName, true
		case "kind":
			return node.typeKind, true
		case "fields":
			out := []any{}
			for _, name := range node.typeFields {
				out = append(out, &gnode{kind: "Payload", values: map[string]any{
					"name": name, "description": nil, "args": []any{},
					"type": &gnode{kind: "Payload", values: map[string]any{"name": "String", "kind": "SCALAR"}},
				}})
			}
			return out, true
		case "enumValues":
			out := []any{}
			for _, name := range node.enumValues {
				out = append(out, &gnode{kind: "Payload", values: map[string]any{"name": name, "description": nil}})
			}
			return out, true
		}
		return nil, false
	case "Schema":
		switch field.name {
		case "queryType":
			return &gnode{kind: "Payload", values: map[string]any{"name": "Query"}}, true
		case "mutationType":
			return &gnode{kind: "Payload", values: map[string]any{"name": "Mutation"}}, true
		case "types":
			return []any{}, true
		}
		return nil, false
	case "Payload":
		if v, ok := node.values[field.name]; ok {
			return v, true
		}
		return nil, false
	}
	return nil, false
}

func executeSelectionSet(s *store, node *gnode, selections []*gselection, ctx *gctx) map[string]any {
	out := map[string]any{}
	for _, sel := range selections {
		if sel.kind == "spread" {
			if frag, ok := ctx.doc.fragments[sel.name]; ok {
				for k, v := range executeSelectionSet(s, node, frag.selections, ctx) {
					out[k] = v
				}
			}
			continue
		}
		if sel.kind == "inline" {
			if sel.typeCondition == "" || sel.typeCondition == gtypeNameOf(node) {
				for k, v := range executeSelectionSet(s, node, sel.selections, ctx) {
					out[k] = v
				}
			}
			continue
		}
		key := sel.name
		if sel.alias != "" {
			key = sel.alias
		}
		if sel.name == "__typename" {
			out[key] = gtypeNameOf(node)
			continue
		}
		value, defined := resolveField(s, node, sel, ctx)
		if !defined {
			ctx.errors = append(ctx.errors, jsonMap{"message": "Cannot query field \"" + sel.name + "\" on type \"" + gtypeNameOf(node) + "\"."})
			out[key] = nil
			continue
		}
		out[key] = completeValue(s, value, sel.selections, ctx)
	}
	return out
}

func completeValue(s *store, value any, selections []*gselection, ctx *gctx) any {
	if value == nil {
		return nil
	}
	if arr, ok := value.([]any); ok {
		out := make([]any, 0, len(arr))
		for _, item := range arr {
			out = append(out, completeValue(s, item, selections, ctx))
		}
		return out
	}
	if n, ok := value.(*gnode); ok {
		if selections == nil {
			return map[string]any{}
		}
		return executeSelectionSet(s, n, selections, ctx)
	}
	return value
}

// executeGraphql runs one operation document.
func executeGraphql(s *store, query string, variables map[string]any, operationName string) jsonMap {
	ctx := &gctx{store: s, variables: variables}
	parser := &gparser{tokens: gtokenize(query)}
	doc, err := parser.parseDocument()
	if err != nil {
		return jsonMap{"data": nil, "errors": []jsonMap{{"message": err.Error()}}}
	}
	ctx.doc = doc
	var operation *goperation
	if operationName != "" {
		for _, op := range doc.operations {
			if op.name == operationName {
				operation = op
				break
			}
		}
	} else if len(doc.operations) > 0 {
		operation = doc.operations[0]
	}
	if operation == nil {
		return jsonMap{"data": nil, "errors": []jsonMap{{"message": "No operation found"}}}
	}
	vars := map[string]any{}
	for k, v := range variables {
		vars[k] = v
	}
	for _, def := range operation.varDefs {
		if _, ok := vars[def.name]; !ok && def.defaultValue != nil {
			vars[def.name] = gvalueFromNode(def.defaultValue, variables)
		}
	}
	ctx.variables = vars
	root := &gnode{kind: "Root", op: operation.operation}
	data := executeSelectionSet(s, root, operation.selections, ctx)
	if len(ctx.errors) > 0 {
		return jsonMap{"data": data, "errors": ctx.errors}
	}
	return jsonMap{"data": data}
}
