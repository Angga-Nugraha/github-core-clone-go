// Store: deterministic scenario identity, in-memory repos/issues/pulls,
// deterministic ids and timestamps (no wall clock, no unseeded randomness).
package main

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- deterministic scenario RNG (mirrors sdk/conformance/wire.ts) ----------

const scenarioSeed uint64 = 0x6a09e667f3bcc909
const u64Mask = ^uint64(0)

func fnv1a64(text string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(text); i++ {
		h ^= uint64(text[i])
		h *= 0x100000001b3
	}
	return h
}

func mix64(v uint64) uint64 {
	z := v + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func scenarioSeedFor(label string) uint64 {
	return mix64(scenarioSeed ^ fnv1a64(label))
}

type rng struct{ state uint64 }

func newRng(seed uint64) *rng {
	masked := seed & u64Mask
	if masked == 0 {
		masked = scenarioSeed
	}
	return &rng{state: masked}
}

func (r *rng) nextU64() uint64 {
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 0x2545f4914f6cdd1d
}

func (r *rng) nextU32() uint32 { return uint32(r.nextU64() >> 32) }

func (r *rng) nextInt(maxExclusive int) int {
	if maxExclusive == 1 {
		return 0
	}
	const range32 = 4294967296
	limit := range32 - (range32 % uint64(maxExclusive))
	for {
		v := uint64(r.nextU32())
		if v < limit {
			return int(v % uint64(maxExclusive))
		}
	}
}

func (r *rng) pick(items []string) string { return items[r.nextInt(len(items))] }

var firstNames = []string{
	"ada", "bram", "cora", "dmitri", "elena", "felix", "greta", "hugo", "iris", "jonas", "kira", "lars",
	"mira", "nico", "olga", "pavel", "quinn", "rosa", "sven", "tess", "ulla", "vito", "wren", "yara", "zane",
}
var lastNames = []string{
	"anders", "bauer", "costa", "dahl", "eriksen", "fischer", "garcia", "holm", "iversen", "jansen", "keller",
	"lind", "moreau", "novak", "olsen", "peters", "quist", "rossi", "sato", "turi", "ulrich", "vega", "weber",
	"xu", "yilmaz", "zimmer",
}

type scenarioUser struct {
	ID    int
	Login string
	Name  string
	Email string
}

func generateUsers(r *rng, count int) []scenarioUser {
	used := map[string]bool{}
	users := make([]scenarioUser, 0, count)
	for i := 0; i < count; i++ {
		first := r.pick(firstNames)
		last := r.pick(lastNames)
		base := first + last
		login := base
		suffix := 2
		for used[login] {
			login = base + "-" + strconv.Itoa(suffix)
			suffix++
		}
		used[login] = true
		users = append(users, scenarioUser{ID: 1000 + i, Login: login, Name: first + " " + last, Email: login + "@example.invalid"})
	}
	return users
}

// ---------- domain types ----------

type gitObject struct {
	Type    string // commit, tree, blob, tag
	Content []byte
}

type repo struct {
	ID            int
	Owner         string
	Name          string
	FullName      string
	Description   string
	Private       bool
	DefaultBranch string
	CreatedAt     string
	UpdatedAt     string
	Refs          map[string]string // full ref name -> commit oid
	Objects       map[string]*gitObject
	Issues        []*issue
	Pulls         []*pull
	Labels        map[string]*label
	NextNumber    int
	Seq           int
}

type label struct {
	ID          int
	Name        string
	Color       string
	Description string
}

type issue struct {
	ID        int
	RepoID    int
	Number    int
	Title     string
	Body      string
	State     string // open | closed
	Labels    []string
	Comments  []*comment
	Seq       int
	CreatedAt string
	UpdatedAt string
	ClosedAt  string
	jsonCache []byte
}

type comment struct {
	ID        int
	IssueID   int
	Body      string
	Seq       int
	CreatedAt string
}

type pull struct {
	ID          int
	RepoID      int
	Number      int
	Title       string
	Body        string
	State       string // open | closed | merged
	Head        string // branch name
	Base        string // branch name
	HeadSHA     string
	BaseSHA     string
	Merged      bool
	MergeCommit string
	MergedAt    string
	Seq         int
	CreatedAt   string
	UpdatedAt   string
	ClosedAt    string
}

// ---------- store ----------

type store struct {
	mu       sync.RWMutex
	token    string
	viewer   scenarioUser
	stranger scenarioUser
	baseMs   int64
	tick     int64
	nextID   int
	seq      int
	repos    []*repo
	byOwner  map[string][]*repo
	byID     map[int]*repo
}

func newStore() *store {
	users := generateUsers(newRng(scenarioSeedFor("users")), 12)
	baseMs := int64(1735689600000) // 2025-01-01T00:00:00Z
	if v := os.Getenv("MAKEFUN_TRIAL_NOW"); v != "" {
		if ms := parseTimeMs(v); ms != 0 {
			baseMs = ms
		}
	}
	return &store{
		token:    os.Getenv("MAKEFUN_SHOP_TOKEN"),
		viewer:   users[0],
		stranger: users[2],
		baseMs:   baseMs,
		nextID:   1,
		repos:    []*repo{},
		byOwner:  map[string][]*repo{},
		byID:     map[int]*repo{},
	}
}

// nowISO returns the next deterministic timestamp (base + 1s per tick).
func (s *store) nowISO() string {
	ms := s.baseMs + s.tick*1000
	s.tick++
	return isoFromMs(ms)
}

// commitTimestamp returns the epoch seconds for the next tick.
func (s *store) commitTimestamp() int64 {
	ms := s.baseMs + s.tick*1000
	s.tick++
	return ms / 1000
}

func (s *store) nextSeq() int {
	s.seq++
	return s.seq
}

func (s *store) nextGlobalID() int {
	id := s.nextID
	s.nextID++
	return id
}

// userJSON is the REST /user viewer object.
func (s *store) userJSON() jsonMap {
	return jsonMap{
		"login":      s.viewer.Login,
		"id":         s.viewer.ID,
		"node_id":    "U_" + strconv.Itoa(s.viewer.ID),
		"avatar_url": "https://example.invalid/avatar/" + s.viewer.Login,
		"type":       "User",
		"name":       s.viewer.Name,
		"email":      s.viewer.Email,
	}
}

// ---------- repos ----------

func (s *store) findRepo(owner, name string) *repo {
	for _, r := range s.byOwner[owner] {
		if r.Name == name {
			return r
		}
	}
	return nil
}

func (s *store) findRepoByID(id int) *repo { return s.byID[id] }

func (s *store) createRepo(owner, name, description string, private bool, defaultBranch string) (*repo, string) {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if s.findRepo(owner, name) != nil {
		return nil, "Repository " + owner + "/" + name + " already exists"
	}
	createdAt := s.nowISO()
	r := &repo{
		ID:            s.nextGlobalID(),
		Owner:         owner,
		Name:          name,
		FullName:      owner + "/" + name,
		Description:   description,
		Private:       private,
		DefaultBranch: defaultBranch,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		Refs:          map[string]string{},
		Objects:       map[string]*gitObject{},
		Issues:        []*issue{},
		Pulls:         []*pull{},
		Labels:        map[string]*label{},
		NextNumber:    1,
		Seq:           s.nextSeq(),
	}
	s.repos = append(s.repos, r)
	s.byOwner[owner] = append(s.byOwner[owner], r)
	s.byID[r.ID] = r
	return r, ""
}

func (s *store) deleteRepo(r *repo) {
	delete(s.byID, r.ID)
	list := s.byOwner[r.Owner]
	for i, x := range list {
		if x == r {
			s.byOwner[r.Owner] = append(list[:i], list[i+1:]...)
			break
		}
	}
	for i, x := range s.repos {
		if x == r {
			s.repos = append(s.repos[:i], s.repos[i+1:]...)
			break
		}
	}
}

// defaultTip returns the default branch tip ("" when unborn).
func (r *repo) defaultTip() string { return r.Refs["refs/heads/"+r.DefaultBranch] }

// getObject returns the git object or nil.
func (r *repo) getObject(oid string) *gitObject { return r.Objects[oid] }

// addObject stores an object, returning its oid.
func (r *repo) addObject(typ string, content []byte) string {
	oid := objectID(typ, content)
	if _, ok := r.Objects[oid]; !ok {
		r.Objects[oid] = &gitObject{Type: typ, Content: content}
	}
	return oid
}

// ---------- issues / pulls ----------

func (s *store) createIssue(r *repo, title, body string, labels []string) (*issue, string) {
	for _, name := range labels {
		if _, ok := r.Labels[name]; !ok {
			return nil, "Label " + name + " does not exist"
		}
	}
	it := &issue{
		ID:        s.nextGlobalID(),
		RepoID:    r.ID,
		Number:    r.NextNumber,
		Title:     title,
		Body:      body,
		State:     "open",
		Labels:    append([]string{}, labels...),
		Comments:  []*comment{},
		Seq:       s.nextSeq(),
		CreatedAt: s.nowISO(),
	}
	it.UpdatedAt = it.CreatedAt
	r.NextNumber++
	r.Issues = append(r.Issues, it)
	return it, ""
}

func (s *store) findIssue(r *repo, number int) *issue {
	for _, it := range r.Issues {
		if it.Number == number {
			return it
		}
	}
	return nil
}

func (s *store) findPull(r *repo, number int) *pull {
	for _, p := range r.Pulls {
		if p.Number == number {
			return p
		}
	}
	return nil
}

func (s *store) createPull(r *repo, title, body, head, base, headSHA, baseSHA string) (*pull, string) {
	headTip := r.Refs["refs/heads/"+head]
	baseTip := r.Refs["refs/heads/"+base]
	if headTip == "" {
		return nil, "Head branch " + head + " does not exist"
	}
	if baseTip == "" {
		return nil, "Base branch " + base + " does not exist"
	}
	for _, p := range r.Pulls {
		if p.State == "open" && p.Head == head && p.Base == base {
			return nil, "A pull request already exists for this head and base"
		}
	}
	p := &pull{
		ID:        s.nextGlobalID(),
		RepoID:    r.ID,
		Number:    r.NextNumber,
		Title:     title,
		Body:      body,
		State:     "open",
		Head:      head,
		Base:      base,
		HeadSHA:   headTip,
		BaseSHA:   baseTip,
		Seq:       s.nextSeq(),
		CreatedAt: s.nowISO(),
	}
	p.UpdatedAt = p.CreatedAt
	r.NextNumber++
	r.Pulls = append(r.Pulls, p)
	return p, ""
}

func (s *store) addComment(it *issue, body string) *comment {
	c := &comment{
		ID:        s.nextGlobalID(),
		IssueID:   it.ID,
		Body:      body,
		Seq:       s.nextSeq(),
		CreatedAt: s.nowISO(),
	}
	it.Comments = append(it.Comments, c)
	it.jsonCache = nil
	return c
}

func (s *store) createLabel(r *repo, name, color, description string) *label {
	if l, ok := r.Labels[name]; ok {
		return l
	}
	if color == "" {
		color = "ededed"
	}
	l := &label{ID: s.nextGlobalID(), Name: name, Color: color, Description: description}
	r.Labels[name] = l
	return l
}

// listIssues returns issues filtered by state, sorted by seq.
func (r *repo) listIssues(state string, desc bool) []*issue {
	out := make([]*issue, 0, len(r.Issues))
	for _, it := range r.Issues {
		if state == "all" || it.State == state {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if desc {
			return out[i].Seq > out[j].Seq
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// listPulls returns pulls filtered by state, sorted by seq desc.
func (r *repo) listPulls(state string) []*pull {
	out := make([]*pull, 0, len(r.Pulls))
	for _, p := range r.Pulls {
		if state == "all" || p.State == state {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	return out
}

// resolveRef resolves a ?ref= value (branch, full ref, or oid) to a tip oid.
func (r *repo) resolveRef(ref string) string {
	if ref == "" {
		return r.defaultTip()
	}
	if tip, ok := r.Refs["refs/heads/"+ref]; ok {
		return tip
	}
	if tip, ok := r.Refs[ref]; ok {
		return tip
	}
	if isOID(ref) {
		if _, ok := r.Objects[ref]; ok {
			return ref
		}
	}
	return ""
}

// ---------- contents / merge ----------

func (s *store) authorLine() (name, email string, ts int64) {
	return s.viewer.Name, s.viewer.Email, s.commitTimestamp()
}

// commitOnBranch creates a commit on branch with the new tree, like Node's
// putContent path: branch missing starts from the default tip.
func (s *store) commitOnBranch(r *repo, branch, message string, newTree map[string]string) string {
	tip := r.Refs["refs/heads/"+branch]
	if tip == "" {
		tip = r.defaultTip()
	}
	name, email, ts := s.authorLine()
	treeOID := buildTree(r, newTree)
	var parents []string
	if tip != "" {
		parents = []string{tip}
	}
	commitOID := addCommit(r, treeOID, parents, name, email, ts, message)
	r.Refs["refs/heads/"+branch] = commitOID
	return commitOID
}

// putContent implements PUT /contents.
// Returns (commitOID, contentSHA, created, status, errMsg).
func (s *store) putContent(r *repo, path, branch, content, message, sha string) (string, string, bool, int, string) {
	if branch == "" {
		branch = r.DefaultBranch
	}
	if message == "" {
		message = "Update " + path
	}
	tip := r.Refs["refs/heads/"+branch]
	if tip == "" {
		tip = r.defaultTip()
	}
	var flat map[string]string
	if tip != "" {
		c := parsedCommit(r, tip)
		if c == nil {
			return "", "", false, 422, "tip commit is missing"
		}
		flat = flattenTree(r, c.Tree)
	} else {
		flat = map[string]string{}
	}
	_, exists := flat[path]
	if exists {
		existingOID := objectID("blob", []byte(flat[path]))
		if sha == "" || !strings.EqualFold(sha, existingOID) {
			return "", "", false, 409, "sha does not match the current blob"
		}
	}
	blobSHA := objectID("blob", []byte(content))
	flat[path] = content
	commitOID := s.commitOnBranch(r, branch, message, flat)
	return commitOID, blobSHA, !exists, 0, ""
}

// deleteContent implements DELETE /contents.
// Returns (commitOID, status, errMsg).
func (s *store) deleteContent(r *repo, path, branch, message string) (string, int, string) {
	if branch == "" {
		branch = r.DefaultBranch
	}
	if message == "" {
		message = "Delete " + path
	}
	tip := r.Refs["refs/heads/"+branch]
	if tip == "" {
		return "", 404, "branch does not exist"
	}
	c := parsedCommit(r, tip)
	if c == nil {
		return "", 422, "tip commit is missing"
	}
	flat := flattenTree(r, c.Tree)
	if _, ok := flat[path]; !ok {
		return "", 404, "file does not exist"
	}
	delete(flat, path)
	return s.commitOnBranch(r, branch, message, flat), 0, ""
}

// mergePull performs the squash merge, mirroring Node's mergePull.
// Uses current branch tips and the merge-base. Returns (sha, ok, errMsg);
// errMsg "conflict" or "closed" map to 405, "missing" to 404.
func (s *store) mergePull(r *repo, p *pull) (string, bool, string) {
	if p.State != "open" {
		return "", false, "closed"
	}
	headTip := r.Refs["refs/heads/"+p.Head]
	baseTip := r.Refs["refs/heads/"+p.Base]
	if headTip == "" || baseTip == "" {
		return "", false, "missing"
	}
	if s.prConflict(r, p) {
		return "", false, "conflict"
	}
	headCommit := parsedCommit(r, headTip)
	baseOID := mergeBase(r, headTip, baseTip)
	if baseOID == "" {
		baseOID = baseTip
	}
	baseCommit := parsedCommit(r, baseOID)
	baseTipCommit := parsedCommit(r, baseTip)
	if headCommit == nil || baseCommit == nil || baseTipCommit == nil {
		return "", false, "missing"
	}
	headFiles := flattenTree(r, headCommit.Tree)
	baseFiles := flattenTree(r, baseCommit.Tree)
	tipFiles := flattenTree(r, baseTipCommit.Tree)
	merged := map[string]string{}
	for k, v := range tipFiles {
		merged[k] = v
	}
	for path, headVal := range headFiles {
		baseVal, inBase := baseFiles[path]
		if !inBase || baseVal != headVal {
			merged[path] = headVal
		}
	}
	for path := range baseFiles {
		if _, inHead := headFiles[path]; !inHead {
			delete(merged, path)
		}
	}
	name, email, ts := s.authorLine()
	treeOID := buildTree(r, merged)
	msg := "Merge pull request #" + strconv.Itoa(p.Number) + " from " + p.Head + "\n\n" + p.Title
	commitOID := addCommit(r, treeOID, []string{baseTip}, name, email, ts, msg)
	r.Refs["refs/heads/"+p.Base] = commitOID
	p.State = "closed"
	p.Merged = true
	p.MergeCommit = commitOID
	p.MergedAt = s.nowISO()
	p.UpdatedAt = p.MergedAt
	p.BaseSHA = commitOID
	return commitOID, true, ""
}

// prConflict mirrors Node's prConflict (merge-base three-way, oid comparison
// over flattened trees; content equality stands in for blob-oid equality).
func (s *store) prConflict(r *repo, p *pull) bool {
	headTip := r.Refs["refs/heads/"+p.Head]
	baseTip := r.Refs["refs/heads/"+p.Base]
	if headTip == "" || baseTip == "" {
		return false
	}
	baseOID := mergeBase(r, headTip, baseTip)
	if baseOID == "" {
		return false
	}
	baseCommit := parsedCommit(r, baseOID)
	headCommit := parsedCommit(r, headTip)
	baseTipCommit := parsedCommit(r, baseTip)
	if baseCommit == nil || headCommit == nil || baseTipCommit == nil {
		return false
	}
	headFiles := flattenTree(r, headCommit.Tree)
	baseFiles := flattenTree(r, baseCommit.Tree)
	tipFiles := flattenTree(r, baseTipCommit.Tree)
	for path, headVal := range headFiles {
		baseVal, inBase := baseFiles[path]
		changedInHead := !inBase || baseVal != headVal
		if !changedInHead {
			continue
		}
		tipVal, inTip := tipFiles[path]
		changedInBase := false
		if !inTip {
			changedInBase = inBase
		} else if !inBase || baseVal != tipVal {
			changedInBase = true
		}
		if !changedInBase {
			continue
		}
		if !inTip || tipVal != headVal {
			return true
		}
	}
	for path, baseVal := range baseFiles {
		if _, inHead := headFiles[path]; inHead {
			continue
		}
		tipVal, inTip := tipFiles[path]
		if !inTip || tipVal != baseVal {
			return true
		}
	}
	return false
}

// pullFiles returns the changed files between the current base and head tips.
func (r *repo) pullFiles(p *pull) []fileChange {
	headTip := r.Refs["refs/heads/"+p.Head]
	baseTip := r.Refs["refs/heads/"+p.Base]
	if headTip == "" || baseTip == "" {
		return []fileChange{}
	}
	headCommit := parsedCommit(r, headTip)
	baseCommit := parsedCommit(r, baseTip)
	if headCommit == nil || baseCommit == nil {
		return []fileChange{}
	}
	return r.diffTrees(baseCommit.Tree, headCommit.Tree)
}

// diffTrees computes added/removed/modified files between two trees.
type fileChange struct {
	Filename  string
	Status    string // added | modified | removed
	Additions int
	Deletions int
}

func (r *repo) diffTrees(baseTreeOID, headTreeOID string) []fileChange {
	base := map[string]string{}
	head := map[string]string{}
	if baseTreeOID != "" {
		base = flattenTree(r, baseTreeOID)
	}
	if headTreeOID != "" {
		head = flattenTree(r, headTreeOID)
	}
	paths := map[string]bool{}
	for k := range base {
		paths[k] = true
	}
	for k := range head {
		paths[k] = true
	}
	out := []fileChange{}
	for path := range paths {
		bv, inBase := base[path]
		hv, inHead := head[path]
		if !inBase && inHead {
			out = append(out, fileChange{Filename: path, Status: "added", Additions: countLines(hv)})
		} else if inBase && !inHead {
			out = append(out, fileChange{Filename: path, Status: "removed", Deletions: countLines(bv)})
		} else if bv != hv {
			a, d := lineDiff(bv, hv)
			out = append(out, fileChange{Filename: path, Status: "modified", Additions: a, Deletions: d})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	return out
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// lineDiff counts added/removed lines by multiset difference.
// Mirrors Node's util lineDiff: split keeps the trailing empty element.
func lineDiff(before, after string) (added, removed int) {
	counts := map[string]int{}
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
	for _, l := range split(before) {
		counts[l]--
	}
	for _, l := range split(after) {
		counts[l]++
	}
	for _, c := range counts {
		if c > 0 {
			added += c
		} else {
			removed += -c
		}
	}
	return added, removed
}

// commitsFrom returns commit oids reachable from start, BFS, newest first.
func (r *repo) commitsFrom(start string) []string {
	out := []string{}
	if start == "" {
		return out
	}
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		c := parsedCommit(r, oid)
		if c == nil {
			continue
		}
		out = append(out, oid)
		queue = append(queue, c.Parents...)
	}
	return out
}

// ensure unused import guard
var _ = time.Now
