// Git core: object parsing/building, pkt-line, packfile read/write,
// reachability, fetch planning, tree flattening.
package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ---------- parsed objects ----------

type parsedCommitT struct {
	Tree    string
	Parents []string
	Author  string
	Email   string
	Time    int64
	Message string
}

func parsedCommit(r *repo, oid string) *parsedCommitT {
	obj := r.getObject(oid)
	if obj == nil || obj.Type != "commit" {
		return nil
	}
	return parseCommitBytes(obj.Content)
}

func parseCommitBytes(content []byte) *parsedCommitT {
	c := &parsedCommitT{}
	// header/body split at blank line
	idx := bytes.Index(content, []byte("\n\n"))
	head := content
	if idx >= 0 {
		head = content[:idx]
		c.Message = string(content[idx+2:])
	}
	for _, line := range strings.Split(string(head), "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			c.Tree = line[5:]
		case strings.HasPrefix(line, "parent "):
			c.Parents = append(c.Parents, line[7:])
		case strings.HasPrefix(line, "author "):
			rest := line[7:]
			if lt := strings.LastIndex(rest, " <"); lt >= 0 {
				c.Author = rest[:lt]
				rest = rest[lt+2:]
				if gt := strings.Index(rest, ">"); gt >= 0 {
					c.Email = rest[:gt]
					rest = strings.TrimSpace(rest[gt+1:])
					if f := strings.Fields(rest); len(f) > 0 {
						c.Time, _ = strconv.ParseInt(f[0], 10, 64)
					}
				}
			}
		}
	}
	return c
}

func buildCommit(tree string, parents []string, name, email string, ts int64, message string) []byte {
	var b strings.Builder
	b.WriteString("tree " + tree + "\n")
	for _, p := range parents {
		b.WriteString("parent " + p + "\n")
	}
	b.WriteString(fmt.Sprintf("author %s <%s> %d +0000\n", name, email, ts))
	b.WriteString(fmt.Sprintf("committer %s <%s> %d +0000\n", name, email, ts))
	b.WriteString("\n")
	b.WriteString(message)
	return []byte(b.String())
}

func addCommit(r *repo, tree string, parents []string, name, email string, ts int64, message string) string {
	return r.addObject("commit", buildCommit(tree, parents, name, email, ts, message))
}

type treeEntry struct {
	Mode string
	Name string
	OID  string
}

func parseTree(content []byte) []treeEntry {
	entries := []treeEntry{}
	for len(content) > 0 {
		sp := bytes.IndexByte(content, ' ')
		if sp < 0 {
			break
		}
		mode := string(content[:sp])
		content = content[sp+1:]
		nul := bytes.IndexByte(content, 0)
		if nul < 0 || len(content) < nul+21 {
			break
		}
		name := string(content[:nul])
		oid := fmt.Sprintf("%x", content[nul+1:nul+21])
		entries = append(entries, treeEntry{Mode: mode, Name: name, OID: oid})
		content = content[nul+21:]
	}
	return entries
}

func buildTreeBytes(entries []treeEntry) []byte {
	// git order: byte order of name, directories compared as name + '/'
	sorted := append([]treeEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return treeSortKey(sorted[i]) < treeSortKey(sorted[j])
	})
	var b bytes.Buffer
	for _, e := range sorted {
		b.WriteString(e.Mode)
		b.WriteByte(' ')
		b.WriteString(e.Name)
		b.WriteByte(0)
		raw, _ := hexDecode(e.OID)
		b.Write(raw)
	}
	return b.Bytes()
}

func treeSortKey(e treeEntry) string {
	if e.Mode == "40000" {
		return e.Name + "/"
	}
	return e.Name
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

// buildTree builds a tree from a flat path->content map, creating intermediate
// trees. Returns the root tree oid.
func buildTree(r *repo, flat map[string]string) string {
	children := map[string]map[string]string{} // dir -> (name -> blob oid)
	dirs := map[string]bool{}
	for path, content := range flat {
		parts := strings.Split(path, "/")
		dir := ""
		for i, part := range parts {
			if i == len(parts)-1 {
				if _, ok := children[dir]; !ok {
					children[dir] = map[string]string{}
				}
				children[dir][part] = r.addObject("blob", []byte(content))
			} else {
				if dir == "" {
					dir = part
				} else {
					dir = dir + "/" + part
				}
				dirs[dir] = true
			}
		}
	}
	// build trees bottom-up: deepest dirs first
	dirList := []string{}
	for d := range dirs {
		dirList = append(dirList, d)
	}
	sort.Slice(dirList, func(i, j int) bool {
		return strings.Count(dirList[i], "/") > strings.Count(dirList[j], "/")
	})
	treeOIDs := map[string]string{} // dir -> tree oid
	for _, d := range dirList {
		entries := []treeEntry{}
		if kids, ok := children[d]; ok {
			for name, oid := range kids {
				entries = append(entries, treeEntry{Mode: "100644", Name: name, OID: oid})
			}
		}
		for sub, oid := range treeOIDs {
			if parentDir(sub) == d {
				entries = append(entries, treeEntry{Mode: "40000", Name: baseName(sub), OID: oid})
			}
		}
		treeOIDs[d] = r.addObject("tree", buildTreeBytes(entries))
	}
	entries := []treeEntry{}
	if kids, ok := children[""]; ok {
		for name, oid := range kids {
			entries = append(entries, treeEntry{Mode: "100644", Name: name, OID: oid})
		}
	}
	for sub, oid := range treeOIDs {
		if parentDir(sub) == "" {
			entries = append(entries, treeEntry{Mode: "40000", Name: baseName(sub), OID: oid})
		}
	}
	return r.addObject("tree", buildTreeBytes(entries))
}

func parentDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// flattenTree returns path -> blob content for a tree oid.
func flattenTree(r *repo, treeOID string) map[string]string {
	out := map[string]string{}
	flattenInto(r, treeOID, "", out)
	return out
}

func flattenInto(r *repo, treeOID, prefix string, out map[string]string) {
	obj := r.getObject(treeOID)
	if obj == nil || obj.Type != "tree" {
		return
	}
	for _, e := range parseTree(obj.Content) {
		path := e.Name
		if prefix != "" {
			path = prefix + "/" + e.Name
		}
		if e.Mode == "40000" {
			flattenInto(r, e.OID, path, out)
		} else {
			if blob := r.getObject(e.OID); blob != nil {
				out[path] = string(blob.Content)
			}
		}
	}
}

// listDir returns the immediate entries of a tree path.
func (r *repo) listDir(treeOID, path string) []treeEntry {
	if path != "" {
		parts := strings.Split(path, "/")
		cur := treeOID
		for _, part := range parts {
			obj := r.getObject(cur)
			if obj == nil || obj.Type != "tree" {
				return nil
			}
			found := ""
			for _, e := range parseTree(obj.Content) {
				if e.Name == part && e.Mode == "40000" {
					found = e.OID
					break
				}
			}
			if found == "" {
				return nil
			}
			cur = found
		}
		treeOID = cur
	}
	obj := r.getObject(treeOID)
	if obj == nil || obj.Type != "tree" {
		return nil
	}
	return parseTree(obj.Content)
}

// lookupPath returns the blob oid at path in a tree, or "".
func (r *repo) lookupPath(treeOID, path string) string {
	parts := strings.Split(path, "/")
	cur := treeOID
	for i, part := range parts {
		obj := r.getObject(cur)
		if obj == nil || obj.Type != "tree" {
			return ""
		}
		found := ""
		var mode string
		for _, e := range parseTree(obj.Content) {
			if e.Name == part {
				found = e.OID
				mode = e.Mode
				break
			}
		}
		if found == "" {
			return ""
		}
		if i == len(parts)-1 {
			if mode == "40000" {
				return ""
			}
			return found
		}
		if mode != "40000" {
			return ""
		}
		cur = found
	}
	return ""
}

// ---------- reachability / graph ----------

// collectReachable returns oids reachable from the given commit oids
// (commits + their trees + blobs), excluding any oid in excluded.
func collectReachable(r *repo, starts []string, excluded map[string]bool) map[string]bool {
	seen := map[string]bool{}
	queue := append([]string{}, starts...)
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] || excluded[oid] {
			continue
		}
		seen[oid] = true
		obj := r.getObject(oid)
		if obj == nil {
			continue
		}
		switch obj.Type {
		case "commit":
			c := parseCommitBytes(obj.Content)
			queue = append(queue, c.Tree)
			queue = append(queue, c.Parents...)
		case "tag":
			if tgt := tagTarget(obj.Content); tgt != "" {
				queue = append(queue, tgt)
			}
		case "tree":
			for _, e := range parseTree(obj.Content) {
				queue = append(queue, e.OID)
			}
		}
	}
	return seen
}

func tagTarget(content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "object ") {
			return line[7:]
		}
		if line == "" {
			break
		}
	}
	return ""
}

// isAncestor returns true when ancestor is reachable from descendant.
func isAncestor(r *repo, ancestor, descendant string) bool {
	if ancestor == descendant {
		return true
	}
	seen := map[string]bool{}
	queue := []string{descendant}
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
		for _, p := range c.Parents {
			if p == ancestor {
				return true
			}
			queue = append(queue, p)
		}
	}
	return false
}

// mergeBase returns the merge-base of two commits (nearest common ancestor).
func mergeBase(r *repo, a, b string) string {
	ancestorsA := map[string]bool{}
	queue := []string{a}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if ancestorsA[oid] {
			continue
		}
		ancestorsA[oid] = true
		if c := parsedCommit(r, oid); c != nil {
			queue = append(queue, c.Parents...)
		}
	}
	queue = []string{b}
	seen := map[string]bool{}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if ancestorsA[oid] {
			return oid
		}
		if c := parsedCommit(r, oid); c != nil {
			queue = append(queue, c.Parents...)
		}
	}
	return ""
}

// ---------- pkt-line ----------

func pktLine(data []byte) []byte {
	out := make([]byte, 4, 4+len(data))
	hexLen := fmt.Sprintf("%04x", len(data)+4)
	copy(out, hexLen)
	return append(out, data...)
}

func pktFlush() []byte { return []byte("0000") }
func pktDelim() []byte { return []byte("0001") }

func pktText(s string) []byte {
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return pktLine([]byte(s))
}

// decodePktLines parses pkt-lines from buf starting at start.
// Returns (packets, ended, afterFlushOffset).
// packets: each entry is raw line content (without the 4-byte length header);
// delim (0001) and response-end (0002) packets appear as their 4-byte form.
// afterFlushOffset is the offset where the pack bytes start (0 when no flush seen).
func decodePktLines(buf []byte, start int) (packets [][]byte, ended bool, afterFlush int) {
	offset := start
	for offset+4 <= len(buf) {
		header := string(buf[offset : offset+4])
		if !isHex(header) {
			return packets, false, 0
		}
		n, err := strconv.ParseInt(header, 16, 32)
		if err != nil || n < 0 {
			return packets, false, 0
		}
		if n == 0 {
			return packets, true, offset + 4
		}
		if n == 1 || n == 2 {
			packets = append(packets, buf[offset:offset+4])
			offset += 4
			continue
		}
		if n < 4 || offset+int(n) > len(buf) {
			return packets, false, 0
		}
		packets = append(packets, buf[offset+4:offset+int(n)])
		offset += int(n)
	}
	return packets, false, 0
}

// sidebandPkt wraps data in a sideband packet (band 1/2/3) as a pkt-line.
func sidebandPkt(band byte, data []byte) []byte {
	out := make([]byte, 0, len(data)+5)
	out = append(out, byte(band))
	out = append(out, data...)
	return pktLine(out)
}

// ---------- packfile ----------

// encodeVarint writes a git variable-length integer (size + type) for pack entries.
func encodeObjectHeader(typ int, size int) []byte {
	out := []byte{byte((typ << 4) | (size & 0x0f))}
	size >>= 4
	for size > 0 {
		out[len(out)-1] |= 0x80
		out = append(out, byte(size&0x7f))
		size >>= 7
	}
	return out
}

var packTypeCodes = map[string]int{"commit": 1, "tree": 2, "blob": 3, "tag": 4}

// writePack serializes objects as a packfile. Objects are sorted by oid
// (deterministic, like Node's writePack).
func writePack(objs map[string]*gitObject) []byte {
	oids := make([]string, 0, len(objs))
	for oid := range objs {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	var b bytes.Buffer
	hasher := newPackHasher(&b)
	hasher.Write([]byte("PACK"))
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], 2)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(oids)))
	hasher.Write(hdr[:])
	// trailer hash covers everything written so far + object data
	for _, oid := range oids {
		obj := objs[oid]
		code := packTypeCodes[obj.Type]
		hasher.Write(encodeObjectHeader(code, len(obj.Content)))
		hasher.Write(zlibDeflate(obj.Content))
	}
	b.Write(hasher.Sum(nil))
	return b.Bytes()
}

// packHasher hashes bytes as they are written to the underlying buffer.
type packHasher struct {
	w  io.Writer
	hh hash.Hash
}

func newPackHasher(w io.Writer) *packHasher {
	return &packHasher{w: w, hh: sha1.New()}
}

func (p *packHasher) Write(b []byte) (int, error) {
	p.hh.Write(b)
	return p.w.Write(b)
}

func (p *packHasher) Sum(b []byte) []byte { return p.hh.Sum(b) }

func zlibDeflate(data []byte) []byte {
	var buf bytes.Buffer
	w, _ := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	w.Write(data)
	w.Close()
	return buf.Bytes()
}

// readPack parses a packfile, resolving OFS/REF deltas against base objects
// from the repo. Returns the new objects (oid -> object).
func readPack(r *repo, pack []byte) (map[string]*gitObject, error) {
	if len(pack) < 12 || string(pack[:4]) != "PACK" {
		return nil, fmt.Errorf("bad pack header")
	}
	version := binary.BigEndian.Uint32(pack[4:8])
	count := binary.BigEndian.Uint32(pack[8:12])
	if version != 2 && version != 3 {
		return nil, fmt.Errorf("unsupported pack version %d", version)
	}
	offset := 12
	type pending struct {
		typ     int
		size    int
		raw     []byte // inflated content (for non-delta)
		base    string // base oid for ref-delta
		baseOff int    // base offset for ofs-delta
		delta   []byte
		off     int
	}
	pendings := make([]*pending, 0, count)
	rawByOffset := map[int]*pending{}
	for i := uint32(0); i < count; i++ {
		if offset >= len(pack) {
			return nil, fmt.Errorf("pack truncated")
		}
		entryOff := offset
		b := pack[offset]
		offset++
		typ := int((b >> 4) & 0x7)
		size := int(b & 0x0f)
		shift := 4
		for b&0x80 != 0 {
			if offset >= len(pack) {
				return nil, fmt.Errorf("pack truncated")
			}
			b = pack[offset]
			offset++
			size |= int(b&0x7f) << shift
			shift += 7
		}
		p := &pending{typ: typ, size: size, off: entryOff}
		if typ == 6 { // ofs-delta
			if offset >= len(pack) {
				return nil, fmt.Errorf("pack truncated")
			}
			b = pack[offset]
			offset++
			baseOff := int(b & 0x7f)
			for b&0x80 != 0 {
				if offset >= len(pack) {
					return nil, fmt.Errorf("pack truncated")
				}
				b = pack[offset]
				offset++
				baseOff = ((baseOff + 1) << 7) | int(b&0x7f)
			}
			p.baseOff = entryOff - baseOff
		} else if typ == 7 { // ref-delta
			if offset+20 > len(pack) {
				return nil, fmt.Errorf("pack truncated")
			}
			p.base = fmt.Sprintf("%x", pack[offset:offset+20])
			offset += 20
		}
		// inflate one zlib stream; bytes.Reader position gives exact end
		br := bytes.NewReader(pack[offset:])
		zr, err := zlib.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("zlib open: %w", err)
		}
		inflated, err := io.ReadAll(zr)
		zr.Close()
		if err != nil {
			return nil, fmt.Errorf("zlib read: %w", err)
		}
		if len(inflated) != size {
			return nil, fmt.Errorf("size mismatch: got %d want %d", len(inflated), size)
		}
		consumed := len(pack[offset:]) - br.Len()
		offset += consumed
		if typ == 6 || typ == 7 {
			p.delta = inflated
		} else {
			p.raw = inflated
		}
		pendings = append(pendings, p)
		rawByOffset[entryOff] = p
	}
	// resolve
	objects := map[string]*gitObject{}
	var resolveOne func(p *pending, depth int) (*gitObject, error)
	resolveOne = func(p *pending, depth int) (*gitObject, error) {
		if depth > 64 {
			return nil, fmt.Errorf("delta chain too deep")
		}
		switch p.typ {
		case 1, 2, 3, 4:
			typName := []string{"", "commit", "tree", "blob", "tag"}[p.typ]
			oid := objectID(typName, p.raw)
			if existing, ok := objects[oid]; ok {
				return existing, nil
			}
			obj := &gitObject{Type: typName, Content: p.raw}
			objects[oid] = obj
			return obj, nil
		case 6:
			baseP, ok := rawByOffset[p.baseOff]
			if !ok {
				return nil, fmt.Errorf("ofs base missing at %d", p.baseOff)
			}
			base, err := resolveOne(baseP, depth+1)
			if err != nil {
				return nil, err
			}
			content, err := applyDelta(base.Content, p.delta)
			if err != nil {
				return nil, err
			}
			oid := objectID(base.Type, content)
			if existing, ok := objects[oid]; ok {
				return existing, nil
			}
			obj := &gitObject{Type: base.Type, Content: content}
			objects[oid] = obj
			return obj, nil
		case 7:
			var base *gitObject
			if existing, ok := objects[p.base]; ok {
				base = existing
			} else if ob := r.getObject(p.base); ob != nil {
				base = ob
			} else {
				// base may be another entry in this pack; find by oid after resolving all?
				return nil, fmt.Errorf("ref base %s missing", p.base)
			}
			content, err := applyDelta(base.Content, p.delta)
			if err != nil {
				return nil, err
			}
			oid := objectID(base.Type, content)
			if existing, ok := objects[oid]; ok {
				return existing, nil
			}
			obj := &gitObject{Type: base.Type, Content: content}
			objects[oid] = obj
			return obj, nil
		}
		return nil, fmt.Errorf("bad type %d", p.typ)
	}
	for _, p := range pendings {
		if _, err := resolveOne(p, 0); err != nil {
			return nil, err
		}
	}
	return objects, nil
}

// applyDelta applies a git delta to base content.
func applyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	readVarint := func() (int, error) {
		v := 0
		shift := 0
		for {
			if pos >= len(delta) {
				return 0, fmt.Errorf("delta truncated")
			}
			b := delta[pos]
			pos++
			v |= int(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				break
			}
		}
		return v, nil
	}
	srcSize, err := readVarint()
	if err != nil {
		return nil, err
	}
	if srcSize != len(base) {
		return nil, fmt.Errorf("delta source size mismatch")
	}
	dstSize, err := readVarint()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, dstSize)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		if op&0x80 != 0 {
			off := 0
			size := 0
			for i := 0; i < 4; i++ {
				if op&(1<<uint(i)) != 0 {
					if pos >= len(delta) {
						return nil, fmt.Errorf("delta truncated")
					}
					off |= int(delta[pos]) << (8 * uint(i))
					pos++
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<uint(i)) != 0 {
					if pos >= len(delta) {
						return nil, fmt.Errorf("delta truncated")
					}
					size |= int(delta[pos]) << (8 * uint(i))
					pos++
				}
			}
			if size == 0 {
				size = 0x10000
			}
			if off+size > len(base) {
				return nil, fmt.Errorf("delta copy out of range")
			}
			out = append(out, base[off:off+size]...)
		} else if op != 0 {
			if pos+int(op) > len(delta) {
				return nil, fmt.Errorf("delta truncated")
			}
			out = append(out, delta[pos:pos+int(op)]...)
			pos += int(op)
		} else {
			return nil, fmt.Errorf("delta opcode 0")
		}
	}
	if len(out) != dstSize {
		return nil, fmt.Errorf("delta result size mismatch")
	}
	return out, nil
}

// ---------- fetch planning ----------

type fetchPlan struct {
	Objects   map[string]*gitObject
	Shallow   []string
	Unshallow []string
}

// planFetch mirrors Node's planFetch: BFS from wants, honoring haves, shallow
// set and depth.
func planFetch(r *repo, wants, haves []string, shallowSet map[string]bool, depth int) *fetchPlan {
	maxDist := 1 << 30
	if depth > 0 {
		maxDist = depth - 1
	}
	excluded := map[string]bool{}
	for _, h := range haves {
		excluded[h] = true
	}
	if depth <= 0 {
		for oid := range collectReachable(r, haves, map[string]bool{}) {
			excluded[oid] = true
		}
	}
	included := map[string]bool{}
	visited := map[string]bool{}
	type item struct {
		oid  string
		dist int
	}
	queue := []item{}
	for _, w := range wants {
		// tags: resolve to target commit
		oid := w
		tagObj := r.getObject(oid)
		if tagObj != nil && tagObj.Type == "tag" {
			if tgt := tagTarget(tagObj.Content); tgt != "" {
				// tag object itself goes into objects if not excluded
				if !excluded[oid] {
					included[oid] = true
				}
				oid = tgt
			}
		}
		queue = append(queue, item{oid, 0})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if visited[it.oid] {
			continue
		}
		visited[it.oid] = true
		if it.dist <= maxDist && !excluded[it.oid] {
			included[it.oid] = true
		}
		if it.dist >= maxDist {
			continue
		}
		if depth <= 0 && shallowSet[it.oid] {
			continue
		}
		c := parsedCommit(r, it.oid)
		if c == nil {
			continue
		}
		for _, p := range c.Parents {
			queue = append(queue, item{p, it.dist + 1})
		}
	}
	objects := map[string]*gitObject{}
	for oid := range included {
		obj := r.getObject(oid)
		if obj == nil {
			continue
		}
		objects[oid] = obj
		if obj.Type != "commit" {
			continue
		}
		c := parseCommitBytes(obj.Content)
		for entryOID := range collectReachable(r, []string{c.Tree}, excluded) {
			if ob := r.getObject(entryOID); ob != nil {
				objects[entryOID] = ob
			}
		}
	}
	plan := &fetchPlan{Objects: objects}
	// shallow / unshallow
	boundary := map[string]bool{}
	for oid := range included {
		c := parsedCommit(r, oid)
		if c == nil {
			continue
		}
		for _, p := range c.Parents {
			if !included[p] {
				boundary[oid] = true
			}
		}
	}
	if len(boundary) > 0 || len(shallowSet) > 0 {
		for oid := range boundary {
			plan.Shallow = append(plan.Shallow, oid)
		}
		sort.Strings(plan.Shallow)
		for oid := range boundary {
			c := parsedCommit(r, oid)
			if c == nil {
				continue
			}
			for _, p := range c.Parents {
				if included[p] {
					plan.Unshallow = append(plan.Unshallow, oid)
					break
				}
			}
		}
		sort.Strings(plan.Unshallow)
	}
	return plan
}

// peeledOID follows a tag to its target commit (or returns the oid itself).
func peeledOID(r *repo, oid string) string {
	obj := r.getObject(oid)
	if obj != nil && obj.Type == "tag" {
		if tgt := tagTarget(obj.Content); tgt != "" {
			return tgt
		}
	}
	return oid
}

// tagPeeled returns the commit oid an annotated tag points at, or "".
func tagPeeled(r *repo, oid string) string {
	obj := r.getObject(oid)
	if obj == nil || obj.Type != "tag" {
		return ""
	}
	tgt := tagTarget(obj.Content)
	if tgt == "" {
		return ""
	}
	if t := r.getObject(tgt); t != nil && t.Type == "commit" {
		return tgt
	}
	return ""
}
