# Majordomo Architecture

Majordomo is an AI-powered repo health grading and PR triage tool for open source maintainers. It runs as a single Go binary on the maintainer's machine, analyzes local repositories using parallel goroutines, and sends structured context to a remote LLM for narrative generation. It optionally connects to a central server for dashboard access, webhook-driven PR triage, and team features.

## Design Principles

**The LLM is the last mile, not the brain.** Go does the expensive data wrangling — file walking, git history, AST-level analysis, grep, blame — all concurrently. The LLM receives a tight, pre-computed prompt and only does what small-to-medium models are good at: generating readable text from structured input.

**Local-first.** The binary works fully offline with `majordomo analyze .`. No account, no server, no network required for core functionality. The server connection is an opt-in upgrade for dashboard access and webhook-driven workflows.

**No embedded inference.** Users run their own LLM server (ollama, LM Studio, llama.cpp) separately. This keeps the binary pure Go with trivial cross-compilation, and lets users pick a model that matches their hardware. A remote 70B through ollama produces dramatically better analysis than an embedded 7B ever would.

**Fan-out collection, single-pass grading.** The analysis pipeline has two distinct phases: parallel data collection (I/O bound, many goroutines) and sequential grading (CPU bound, pure computation on pre-collected data). The grading engine never touches the file system.

## Project Structure

```
majordomo/
├── cmd/
│   └── majordomo/
│       └── main.go                 # CLI entrypoint, flag parsing, command dispatch
├── internal/
│   ├── analyze/
│   │   ├── collector.go            # Parallel data collection engine
│   │   └── analyze.go              # Run pipeline, LLM prompt builder, report printer
│   ├── config/
│   │   └── config.go               # TOML config, keyring auth, device flow login, setup
│   ├── grade/
│   │   └── grade.go                # Scoring engine (pure data in, scorecard out)
│   ├── llm/
│   │   └── llm.go                  # Remote LLM client with auto-detection
│   ├── repo/
│   │   └── repo.go                 # File system operations, grep, git helpers
│   └── worker/
│       └── worker.go               # Server poll loop for remote jobs
└── install.sh                      # curl-pipe installer
```

## Data Flow

The core pipeline flows in one direction with clear boundaries between I/O and computation:

```
File System + Git ──► Collector (goroutines) ──► RepoData struct
                                                      │
                                                      ▼
                                                 grade.Input
                                               (no file access)
                                                      │
                                                      ▼
                                                grade.Report
                                                (scorecard)
                                                      │
                                                      ▼
                                               BuildPrompt()
                                            (structured text)
                                                      │
                                                      ▼
                                              LLM (remote)
                                                      │
                                                      ▼
                                                 Narrative
```

Each layer only sees the data it needs. The grading engine receives a `grade.Input` struct with pre-computed booleans and counts — it has no knowledge of file paths, git repositories, or the LLM. The prompt builder receives both `RepoData` (for file-level detail) and `grade.Report` (for the scorecard) and assembles a single string.

## Parallel Collection

The `analyze.Collect` function is the performance-critical path. It runs in two phases:

**Phase 1: File walk.** A single goroutine walks the file tree, building the `[]FileEntry` slice that everything else depends on. This must complete before phase 2 begins.

**Phase 2: Fan-out.** Once the file list exists, ~15 goroutines launch concurrently via `errgroup`:

- Git log parsing (shells out to `git log`)
- TODO/FIXME/HACK grep across all files
- Doc comment ratio calculation
- Structure checks (CI, linter, formatter, lockfile, CODEOWNERS, etc.)
- Integration test detection (grep test files for framework markers)
- CI config analysis (grep workflow files for test commands)
- Per-file deep analysis (bounded to 8 concurrent goroutines)

All goroutines write to `RepoData` through a mutex. The per-file analysis uses `errgroup.SetLimit(8)` to avoid disk thrashing on large repositories.

```
Phase 1                    Phase 2

WalkFiles ──────┬──► gitLog
                ├──► grepTODOs
                ├──► docCommentRatio
                ├──► structureChecks (HasCI, HasLinter, ...)
                ├──► integrationTestDetection
                ├──► ciConfigAnalysis
                └──► perFileAnalysis ──► [8 bounded goroutines]
                                              ├── analyzeFile(a.go)
                                              ├── analyzeFile(b.py)
                                              ├── analyzeFile(c.ts)
                                              └── ...
```

## Grading Engine

The grader is a pure function: `grade.FromData(input *grade.Input) *grade.Report`. It evaluates six categories, each containing a list of boolean signals:

- **AI Readiness** — CONTRIBUTING.md, PR templates, CODEOWNERS, AI context files (.cursorrules, CLAUDE.md), architecture docs, conventional commits
- **Guardrails** — CI, linter, formatter, pre-commit hooks, lockfile, security scanning
- **Test Quality** — test file existence, test-to-source ratio, integration tests, tests in CI
- **Documentation** — README size, API spec, inline doc coverage, ADRs, changelog, setup instructions
- **Contribution Hygiene** — direct push frequency, bus factor, recent activity
- **Maintainability** — TODO count, large file count, dependency count, commit recency

Each signal produces a `Signal{Name, Passed, Detail}`. The category score is the count of passing signals divided by total signals. The overall grade is the weighted sum across categories, mapped to a letter grade.

## LLM Integration

The `llm.Client` interface has one method: `Generate(ctx, prompt) (string, error)`. The single implementation (`Remote`) speaks the OpenAI-compatible `/v1/chat/completions` endpoint, which ollama, LM Studio, and llama.cpp all expose.

Auto-detection probes localhost ports 11434 (ollama), 1234 (LM Studio), and 8080 (llama.cpp) with a 2-second timeout. The first to respond wins. If nothing is found, the tool runs in stats-only mode — the scorecard still works, just without the narrative.

The prompt sent to the LLM contains the full scorecard with pass/fail signals, notable file-level issues (lint suppression, oversized files), and high-complexity file listings. The LLM's only job is to write a readable report card from this structured input.

## Server Architecture

The server is optional. When connected, the architecture looks like this:

```
GitHub ──webhook──► Cloud Run ──INSERT──► Postgres
                        ▲
                        │ HTTPS poll
                        │
                    Go binary
                  (maintainer's machine)
```

**Cloud Run** is stateless. It receives GitHub webhooks, writes a row to `job_queue`, and goes back to sleep. It also serves the dashboard UI, the worker poll endpoint, and the result submission endpoint. It scales to zero.

**Postgres** is the message bus. The `job_queue` table uses `SELECT FOR UPDATE SKIP LOCKED` for safe concurrent job claiming. Workers poll `POST /api/workers/poll`, which claims a pending job and returns it. Results go back via `POST /api/workers/result`.

**The worker binary** authenticates via OAuth device flow (stored in the system keyring), registers its watched repos, and enters a poll loop. When it claims a job, it runs the same `Collect → Grade → LLM` pipeline and posts the result back.

Workers never have direct database access. External maintainers talk exclusively to the REST API. The internal mac-mini worker could optionally connect to Postgres directly for lower latency, but the API path is the default.

## Job Queue

```sql
CREATE TABLE job_queue (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        TEXT NOT NULL,
    payload     JSONB NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    worker_id   TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX idx_job_queue_pending ON job_queue (created_at)
    WHERE status = 'pending';
```

The claim query is atomic:

```sql
UPDATE job_queue
SET status = 'running', worker_id = $1, started_at = now()
WHERE id = (
    SELECT id FROM job_queue
    WHERE status = 'pending'
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
) RETURNING *;
```

`SKIP LOCKED` prevents workers from contending on the same row. `LIMIT 1` enforces one-job-at-a-time on each worker.

## Real-Time Updates

For dashboard live updates, Postgres `LISTEN/NOTIFY` bridges to WebSocket clients when a persistent server is available. A trigger on the results table fires `pg_notify` on insert, the server's dedicated listener connection receives it, and a hub broadcasts to connected WebSocket clients.

When no persistent server is available (e.g., Cloud Run scales to zero), the frontend falls back to polling `GET /api/results?since={timestamp}`.

For the webhook delivery path, Cloud Run inserts the webhook payload into `job_queue`. The worker picks it up on its next poll cycle. No persistent connection is required from the server to the worker.

## Authentication

Workers authenticate via OAuth 2.0 device flow, modeled after the GitHub CLI pattern:

1. Worker requests a device code from `POST /api/auth/device`
2. User opens the verification URL and enters the code
3. Worker polls `POST /api/auth/device/token` until authorized
4. Token is stored in the system keyring via `go-keyring`

The token scopes which repos a worker can claim jobs for. The server enforces this during the poll query.

## Distribution

The binary is pure Go with no CGO dependencies, making cross-compilation trivial. Release builds target:

- `darwin-amd64` (Intel Mac)
- `darwin-arm64` (Apple Silicon)
- `linux-amd64`
- `linux-arm64`
- `windows-amd64`

Users install via:

```bash
curl -fsSL https://julython.org/majordomo/install.sh | bash
majordomo setup
```

The install script detects the platform, downloads the correct binary to `~/.local/bin`, and offers to add it to PATH. The `setup` command handles LLM server detection and optional server authentication interactively.

## Mobile App

The mobile app is a read-and-decide interface, not a compute node. It receives push notifications when a PR triage report needs attention, displays the worker's pre-computed analysis (score, signals, narrative), and provides action buttons (close, request changes, ask, approve) that hit the server API, which talks to the GitHub API.

The app never runs inference, clones repos, or does file system analysis. It is a thin REST client over the same API the dashboard uses.
