// Git smart HTTP: info/refs advertisements (v0/v2), upload-pack (v0/v2),
// receive-pack. Mirrors the Node git-http.ts behavior exactly.
package main

import (
	"bytes"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const agentCap = "agent=github-core-clone/1.0.0"
const uploadCapsV0 = "side-band-64k thin-pack ofs-delta multi_ack " + agentCap
const receiveCapsV0 = "report-status side-band-64k delete-refs " + agentCap

type refPair struct {
	name string
	oid  string
}

func sortedRefs(r *repo) []refPair {
	refs := make([]refPair, 0, len(r.Refs))
	for name, oid := range r.Refs {
		refs = append(refs, refPair{name, oid})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	return refs
}

func headOID(r *repo) string { return r.defaultTip() }

// ---------- fetch cache ----------

var fetchCache = struct {
	sync.Mutex
	m map[string][]byte
}{m: map[string][]byte{}}

type fetchArgs struct {
	wants   []string
	haves   []string
	shallow []string
	depth   int // 0 = unset
}

func parseV2Fetch(lines []string) fetchArgs {
	args := fetchArgs{}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "want "):
			args.wants = append(args.wants, strings.TrimSpace(line[5:]))
		case strings.HasPrefix(line, "have "):
			args.haves = append(args.haves, strings.TrimSpace(line[5:]))
		case strings.HasPrefix(line, "shallow "):
			args.shallow = append(args.shallow, strings.TrimSpace(line[8:]))
		case strings.HasPrefix(line, "deepen "):
			if d, err := strconv.Atoi(strings.TrimSpace(line[7:])); err == nil && d > 0 {
				args.depth = d
			}
		}
	}
	return args
}

// sidebandPackets splits pack bytes into sideband band-1 pkt-lines (65515 max).
func sidebandPackets(pack []byte) [][]byte {
	const chunk = 65515
	out := [][]byte{}
	for off := 0; off < len(pack); off += chunk {
		end := off + chunk
		if end > len(pack) {
			end = len(pack)
		}
		payload := make([]byte, 0, end-off+1)
		payload = append(payload, 1)
		payload = append(payload, pack[off:end]...)
		out = append(out, pktLine(payload))
	}
	return out
}

func buildFetchResponse(r *repo, args fetchArgs, v2 bool) []byte {
	key := strconv.Itoa(r.ID) + "|" + map[bool]string{true: "2", false: "0"}[v2] + "|" +
		strings.Join(args.wants, ",") + "|" + strings.Join(args.haves, ",") + "|" +
		strings.Join(args.shallow, ",") + "|" + strconv.Itoa(args.depth)
	fetchCache.Lock()
	if cached, ok := fetchCache.m[key]; ok {
		fetchCache.Unlock()
		return cached
	}
	fetchCache.Unlock()

	resp := buildFetchResponseUncached(r, args, v2)

	allKnown := true
	for _, w := range args.wants {
		if r.getObject(w) == nil {
			allKnown = false
			break
		}
	}
	if allKnown {
		for _, h := range args.haves {
			if r.getObject(h) == nil {
				allKnown = false
				break
			}
		}
	}
	if allKnown {
		for _, o := range args.shallow {
			if r.getObject(o) == nil {
				allKnown = false
				break
			}
		}
	}
	if allKnown {
		fetchCache.Lock()
		if len(fetchCache.m) >= 64 {
			fetchCache.m = map[string][]byte{}
		}
		fetchCache.m[key] = resp
		fetchCache.Unlock()
	}
	return resp
}

func buildFetchResponseUncached(r *repo, args fetchArgs, v2 bool) []byte {
	shallowSet := map[string]bool{}
	for _, o := range args.shallow {
		shallowSet[o] = true
	}
	plan := planFetch(r, args.wants, args.haves, shallowSet, args.depth)
	var chunks [][]byte
	if v2 {
		chunks = append(chunks, pktText("acknowledgments\n"))
		chunks = append(chunks, pktText("NAK\n"))
		if len(plan.Objects) == 0 {
			chunks = append(chunks, pktFlush())
			return bytes.Join(chunks, nil)
		}
		chunks = append(chunks, pktDelim())
		if len(plan.Shallow) > 0 || len(plan.Unshallow) > 0 {
			chunks = append(chunks, pktText("shallow-info\n"))
			for _, oid := range plan.Shallow {
				chunks = append(chunks, pktText("shallow "+oid+"\n"))
			}
			for _, oid := range plan.Unshallow {
				chunks = append(chunks, pktText("unshallow "+oid+"\n"))
			}
			chunks = append(chunks, pktDelim())
		}
		chunks = append(chunks, pktText("packfile\n"))
		chunks = append(chunks, sidebandPackets(writePack(plan.Objects))...)
		chunks = append(chunks, pktFlush())
		return bytes.Join(chunks, nil)
	}
	// v0
	chunks = append(chunks, pktText("NAK\n"))
	if len(plan.Objects) == 0 {
		chunks = append(chunks, pktFlush())
		return bytes.Join(chunks, nil)
	}
	for _, oid := range plan.Shallow {
		chunks = append(chunks, pktText("shallow "+oid+"\n"))
	}
	for _, oid := range plan.Unshallow {
		chunks = append(chunks, pktText("unshallow "+oid+"\n"))
	}
	chunks = append(chunks, sidebandPackets(writePack(plan.Objects))...)
	chunks = append(chunks, pktFlush())
	return bytes.Join(chunks, nil)
}

// ---------- advertisements ----------

func infoRefsUploadPack(r *repo, v2 bool) []byte {
	if v2 {
		chunks := [][]byte{
			pktText("version 2\n"),
			pktText(agentCap + "\n"),
			pktText("ls-refs=unborn\n"),
			pktText("fetch=shallow filter\n"),
			pktText("object-format=sha1\n"),
			pktFlush(),
		}
		return bytes.Join(chunks, nil)
	}
	chunks := [][]byte{pktText("# service=git-upload-pack\n"), pktFlush()}
	head := headOID(r)
	refs := sortedRefs(r)
	if head == "" {
		chunks = append(chunks, pktText(zeroOID+" capabilities^{}\x00symref=HEAD:refs/heads/"+r.DefaultBranch+" "+uploadCapsV0+"\n"))
	} else {
		chunks = append(chunks, pktText(head+" HEAD\x00symref=HEAD:refs/heads/"+r.DefaultBranch+" "+uploadCapsV0+"\n"))
		for _, ref := range refs {
			chunks = append(chunks, pktText(ref.oid+" "+ref.name+"\n"))
			if peeled := tagPeeled(r, ref.oid); peeled != "" {
				chunks = append(chunks, pktText(peeled+" "+ref.name+"^{}\n"))
			}
		}
	}
	chunks = append(chunks, pktFlush())
	return bytes.Join(chunks, nil)
}

func infoRefsReceivePack(r *repo) []byte {
	chunks := [][]byte{pktText("# service=git-receive-pack\n"), pktFlush()}
	head := headOID(r)
	refs := sortedRefs(r)
	if head == "" {
		chunks = append(chunks, pktText(zeroOID+" capabilities^{}\x00"+receiveCapsV0+"\n"))
	} else {
		chunks = append(chunks, pktText(head+" HEAD\x00"+receiveCapsV0+"\n"))
		for _, ref := range refs {
			chunks = append(chunks, pktText(ref.oid+" "+ref.name+"\n"))
			if peeled := tagPeeled(r, ref.oid); peeled != "" {
				chunks = append(chunks, pktText(peeled+" "+ref.name+"^{}\n"))
			}
		}
	}
	chunks = append(chunks, pktFlush())
	return bytes.Join(chunks, nil)
}

// ---------- upload-pack ----------

func isDelimPacket(p []byte) bool { return len(p) == 4 && (string(p) == "0001" || string(p) == "0002") }

func uploadPackV2(r *repo, body []byte) []byte {
	packets, _, _ := decodePktLines(body, 0)
	type entry struct {
		delim bool
		text  string
	}
	entries := []entry{}
	for _, p := range packets {
		if isDelimPacket(p) {
			entries = append(entries, entry{delim: true})
		} else {
			entries = append(entries, entry{text: string(p)})
		}
	}
	command := ""
	delimIndex := -1
	for i, e := range entries {
		if !e.delim && strings.HasPrefix(e.text, "command=") {
			command = strings.TrimSpace(e.text[len("command="):])
		}
		if e.delim && delimIndex < 0 {
			delimIndex = i
		}
	}
	var argLines []string
	if delimIndex >= 0 {
		for _, e := range entries[delimIndex+1:] {
			if !e.delim {
				argLines = append(argLines, e.text)
			}
		}
	}

	if command == "ls-refs" {
		peel, symrefs, unborn := false, false, false
		var prefixes []string
		for _, raw := range argLines {
			line := strings.TrimSpace(raw)
			switch {
			case line == "peel":
				peel = true
			case line == "symrefs":
				symrefs = true
			case line == "unborn":
				unborn = true
			case strings.HasPrefix(line, "ref-prefix "):
				prefixes = append(prefixes, strings.TrimSpace(line[len("ref-prefix "):]))
			}
		}
		var chunks [][]byte
		head := headOID(r)
		refs := sortedRefs(r)
		if head == "" {
			if unborn {
				chunks = append(chunks, pktText("unborn HEAD symref-target:refs/heads/"+r.DefaultBranch+"\n"))
			}
		} else {
			includeHead := len(prefixes) == 0
			for _, p := range prefixes {
				if strings.HasPrefix("HEAD", p) {
					includeHead = true
				}
			}
			if includeHead {
				line := head + " HEAD"
				if symrefs {
					line += " symref-target:refs/heads/" + r.DefaultBranch
				}
				chunks = append(chunks, pktText(line+"\n"))
			}
			for _, ref := range refs {
				if len(prefixes) > 0 {
					ok := false
					for _, p := range prefixes {
						if strings.HasPrefix(ref.name, p) {
							ok = true
						}
					}
					if !ok {
						continue
					}
				}
				line := ref.oid + " " + ref.name
				if peel {
					if peeled := tagPeeled(r, ref.oid); peeled != "" {
						line += " peeled:" + peeled
					}
				}
				chunks = append(chunks, pktText(line+"\n"))
			}
		}
		chunks = append(chunks, pktFlush())
		return bytes.Join(chunks, nil)
	}

	if command == "fetch" {
		args := parseV2Fetch(argLines)
		return buildFetchResponse(r, args, true)
	}

	return []byte("0000")
}

func uploadPackV0(r *repo, body []byte) []byte {
	first, ended, afterFlush := decodePktLines(body, 0)
	var second [][]byte
	if ended {
		second, _, _ = decodePktLines(body, afterFlush)
	}
	textOf := func(packets [][]byte) []string {
		out := []string{}
		for _, p := range packets {
			if !isDelimPacket(p) {
				out = append(out, string(p))
			}
		}
		return out
	}
	args := fetchArgs{}
	for _, line := range textOf(first) {
		switch {
		case strings.HasPrefix(line, "want "):
			rest := line[5:]
			if i := strings.Index(rest, "\x00"); i >= 0 {
				rest = rest[:i]
			}
			args.wants = append(args.wants, strings.TrimSpace(rest))
		case strings.HasPrefix(line, "shallow "):
			args.shallow = append(args.shallow, strings.TrimSpace(line[8:]))
		case strings.HasPrefix(line, "deepen "):
			if d, err := strconv.Atoi(strings.TrimSpace(line[7:])); err == nil && d > 0 {
				args.depth = d
			}
		}
	}
	for _, line := range textOf(second) {
		if strings.HasPrefix(line, "have ") {
			args.haves = append(args.haves, strings.TrimSpace(line[5:]))
		}
	}
	return buildFetchResponse(r, args, false)
}

// ---------- receive-pack ----------

type pushCommand struct {
	old, next, ref string
}

func isOID(s string) bool { return len(s) == 40 && isHex(s) }

func receivePack(r *repo, body []byte) []byte {
	packets, ended, afterFlush := decodePktLines(body, 0)
	var commands []pushCommand
	for _, p := range packets {
		if isDelimPacket(p) {
			continue
		}
		line := string(p)
		if i := strings.Index(line, "\x00"); i >= 0 {
			line = line[:i]
		}
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) >= 3 && isOID(parts[0]) && isOID(parts[1]) {
			commands = append(commands, pushCommand{old: parts[0], next: parts[1], ref: parts[2]})
		}
	}
	var packBytes []byte
	if ended {
		packBytes = body[afterFlush:]
	}
	unpackOk := true
	if len(packBytes) > 0 {
		objects, err := readPack(r, packBytes)
		if err != nil {
			unpackOk = false
		} else {
			for oid, obj := range objects {
				if _, ok := r.Objects[oid]; !ok {
					r.Objects[oid] = obj
				}
			}
		}
	}

	var report []string
	if unpackOk {
		report = append(report, "unpack ok\n")
	} else {
		report = append(report, "unpack failed\n")
	}
	for _, cmd := range commands {
		if !unpackOk && len(packBytes) > 0 {
			report = append(report, "ng "+cmd.ref+" unpack failed\n")
			continue
		}
		current, exists := r.Refs[cmd.ref]
		if cmd.next == zeroOID {
			if !exists {
				report = append(report, "ng "+cmd.ref+" cannot delete non-existent ref\n")
			} else if cmd.old != zeroOID && cmd.old != current {
				report = append(report, "ng "+cmd.ref+" non-fast-forward\n")
			} else {
				delete(r.Refs, cmd.ref)
				report = append(report, "ok "+cmd.ref+"\n")
			}
			continue
		}
		if cmd.old == zeroOID {
			if exists {
				report = append(report, "ng "+cmd.ref+" already exists\n")
				continue
			}
			if _, ok := r.Objects[cmd.next]; !ok {
				report = append(report, "ng "+cmd.ref+" object not found\n")
				continue
			}
			r.Refs[cmd.ref] = cmd.next
			report = append(report, "ok "+cmd.ref+"\n")
			continue
		}
		if !exists || cmd.old != current {
			report = append(report, "ng "+cmd.ref+" non-fast-forward\n")
			continue
		}
		ancestor := false
		if nc := parsedCommit(r, cmd.next); nc != nil {
			for _, p := range nc.Parents {
				if p == cmd.old {
					ancestor = true
					break
				}
			}
			if !ancestor {
				ancestor = isAncestor(r, cmd.old, cmd.next)
			}
		}
		if !ancestor {
			report = append(report, "ng "+cmd.ref+" non-fast-forward\n")
			continue
		}
		r.Refs[cmd.ref] = cmd.next
		report = append(report, "ok "+cmd.ref+"\n")
	}

	var chunks [][]byte
	for _, line := range report {
		payload := append([]byte{1}, []byte(line)...)
		chunks = append(chunks, pktLine(payload))
	}
	chunks = append(chunks, pktFlush())
	return bytes.Join(chunks, nil)
}

// ---------- router ----------

// handleGitHTTP routes a git smart-HTTP request. Returns (status, contentType, body).
func handleGitHTTP(s *store, segments []string, query url.Values, method string, body []byte, v2 bool) (int, string, []byte) {
	if len(segments) < 2 || !strings.HasSuffix(segments[1], ".git") {
		return 404, jsonCT, errJSON("Not Found")
	}
	repoName := segments[1][:len(segments[1])-4]
	r := s.findRepo(segments[0], repoName)
	if r == nil {
		return 404, jsonCT, errJSON("Not Found")
	}
	tail := segments[2:]
	if method == "GET" && len(tail) == 2 && tail[0] == "info" && tail[1] == "refs" {
		service := query.Get("service")
		if service == "git-upload-pack" {
			return 200, "application/x-git-upload-pack-advertisement", infoRefsUploadPack(r, v2)
		}
		if service == "git-receive-pack" {
			return 200, "application/x-git-receive-pack-advertisement", infoRefsReceivePack(r)
		}
		return 400, jsonCT, errJSON("service is required")
	}
	if method == "POST" && len(tail) == 1 && tail[0] == "git-upload-pack" {
		if v2 {
			return 200, "application/x-git-upload-pack-result", uploadPackV2(r, body)
		}
		return 200, "application/x-git-upload-pack-result", uploadPackV0(r, body)
	}
	if method == "POST" && len(tail) == 1 && tail[0] == "git-receive-pack" {
		return 200, "application/x-git-receive-pack-result", receivePack(r, body)
	}
	return 404, jsonCT, errJSON("Not Found")
}
