# github-core-clone-go

A deterministic, stdlib-only GitHub core clone written in Go: git smart HTTP,
REST v3, GraphQL, and MCP in a single statically linked binary. Built for the
make.fun bounty [github-core-clone-01](https://make.fun/bounty/github-core-clone-01).

[![github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/Angga-Nugraha/github-core-clone-go/rank.svg)](https://make.fun/bounty/github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/Angga-Nugraha/github-core-clone-go/reward.svg)](https://make.fun/bounty/github-core-clone-01)

This is a Go rewrite of the Node entry
[github-core-clone](https://github.com/Angga-Nugraha/github-core-clone). The
Node entry scored 939 (feature 600, agent 200, perf 139) — the gap to the
prize zone was purely per-request latency. Go's `net/http` server closes it:
practice grade-local scores **996** (feature 600, agent 200, perf 196,
perf_p50 170,748us).

## What it does

- **Git smart HTTP** (`/{owner}/{repo}.git`): upload-pack protocol v2
  (ls-refs, fetch, shallow/deepen) and v0, receive-pack v0 with
  fast-forward-only pushes. Packfile parsing/packing, pkt-line framing, and
  the object store (zlib, SHA-1) are all implemented by hand — no git binary,
  no external modules.
- **REST v3** (`/api/v3`): repositories, branches, commits, refs, contents,
  labels, issues, comments, pull requests, and squash merges, with
  GitHub-shaped pagination (`Link: rel="next"`).
- **GraphQL** (`POST /api/graphql`): viewer, repository, issue/PR connections
  with cursor pagination, `__type` introspection, and the `createIssue` /
  `closeIssue` / `createPullRequest` / `mergePullRequest` / `addComment`
  mutations.
- **MCP** (`POST /mcp`): JSON-RPC 2.0 Streamable HTTP, revision `2026-07-28`
  (`server/discover`) and the legacy `initialize` handshake, with seven tools
  (`create_repo`, `get_repo`, `create_issue`, `list_issues`, `get_issue`,
  `create_pull_request`, `merge_pull_request`) acting on the same in-memory
  store as REST.

## Determinism

One process, one in-memory store. Dates come from `MAKEFUN_TRIAL_NOW` (never
the wall clock) and ids from counters seeded at startup — no unseeded
randomness — so a trial replays byte-identically.

## Run it

```sh
./forge
```

The grader runs `node /bot/main.ts`; `main.ts` is a thin launcher that spawns
the committed `./forge` binary (statically linked linux/amd64, built with
`CGO_ENABLED=0`) and waits for it. `MAKEFUN_SHOP_TOKEN` sets the Bearer token
the REST, GraphQL, and MCP surfaces accept; `MAKEFUN_TRIAL_NOW` sets the
logical clock. With `MAKEFUN_ENV_URL` set, the process registers its port
with the harness and exits when the harness reports done.

## Layout

- `main.ts` — entry point: spawns `./forge` and waits.
- `forge` — the built Go binary (linux/amd64, static).
- `go/main.go` — lifecycle (bind, register, poll done, exit) and HTTP routing.
- `go/git.go` — git object model, pkt-lines, packfiles, graph walks.
- `go/githttp.go` — git smart-HTTP handlers.
- `go/store.go` — in-memory store: repos, issues, PRs, contents, merges.
- `go/rest.go` — REST v3 handlers.
- `go/graphql.go` — GraphQL parser + executor.
- `go/mcp.go` — MCP JSON-RPC handlers and tools.
- `go/util.go` — helpers: base64url ids, JSON encoding, request ids.

## Build it

```sh
cd go
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../forge .
```
