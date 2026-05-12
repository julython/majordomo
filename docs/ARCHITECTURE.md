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
│   ├── commands/
│   │   ├── registry.go            # Command registry, parsing, and Sink interface
│   │   └── builtins.go            # Built-in command implementations
│   ├── config/
│   │   └── config.go               # TOML config, keyring auth, device flow login, setup
│   ├── grade/
│   │   └── grade.go                # Scoring engine (pure data in, scorecard out)
│   ├── jobs/
│   │   └── tracker.go              # Cancellable job lifecycle management
│   ├── knowledge/
│   │   └── store.go                # On-disk knowledge base (.majordomo/knowledge.json)
│   ├── llm/
│   │   └── llm.go                  # Remote LLM client with auto-detection
│   ├── mcp/
│   │   ├── server.go              # MCP server with tool definitions
│   │   └── transport.go           # Stdio JSON-RPC transport
│   ├── mdrender/
│   │   └── mdrender.go             # Terminal markdown rendering with Glamour
│   ├── repo/
│   │   └── repo.go                 # File system operations, grep, git helpers
│   ├── tui/
│   │   ├── app.go                 # Main TUI app with Chat/Config mode switching
│   │   ├── chat.go                # Chat interface with command execution and markdown
│   │   ├── config.go              # Interactive configuration editor
│   │   └── sink.go                # TUI StreamSink and CLI CLISink implementations
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

The `llm.Client` interface supports both batch and streaming generation:

```go
type Client interface {
    Generate(ctx context.Context, prompt string) (string, error)
    Stream(ctx context.Context, prompt string, onChunk func(token string)) (string, error)
    Name() string
}
```

The `LocalClient` implementation speaks the OpenAI-compatible `/v1/chat/completions` endpoint, which ollama, LM Studio, and llama.cpp all expose. It auto-detects local LLM servers by probing localhost ports:

| Provider   | Port  | Default Model |
|------------|-------|---------------|
| ollama     | 11434 | llama3.2      |
| LM Studio  | 1234  | default       |
| llama.cpp  | 8080  | default       |

Each probe has a 2-second timeout. The first to respond wins. If nothing is found, the tool runs in stats-only mode — the scorecard still works, just without the narrative.

**Streaming:** The `Stream()` method enables token-by-token rendering. The TUI buffers tokens until a newline, then calls `PrintMarkdown()` for smooth progressive display. If no callback is provided, `Stream()` falls back to `Generate()` behavior.

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

**Launch modes:** Running `majordomo` with no arguments starts the interactive TUI. Providing arguments (e.g., `majordomo analyze .`) runs in CLI mode.

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

## Job Tracker

The `jobs.Tracker` manages cancellable job lifecycles for long-running operations. Both the CLI probe runner and the watch worker use this.

```go
type Job struct {
    ID        string
    Kind      string
    Status    Status  // pending, running, done, failed, cancelled
    StartedAt time.Time
    EndedAt   time.Time
    Error     string
}
```

**Lifecycle:**

```
Start() ──► context + cancel func ──► [work runs] ──► Complete() or Cancel()
              │                                                 │
              └───────────────── context cancel ───────────────┘
```

**Key methods:**

- `Start(ctx, id, kind)` — registers a job, returns a cancellable context
- `Complete(id, err)` — marks done/failed, cleans up cancel func
- `Cancel(id)` — stops a running job, marks it cancelled
- `CancelAll()` — stops all jobs (used on shutdown/Ctrl+C)
- `Prune(olderThan)` — removes old completed jobs from memory

Jobs track their own cancellation via a `context.CancelFunc`. When cancelled, the context propagates to `exec.CommandContext`, HTTP calls, and LLM generation — no per-subsystem cancellation needed.

## Knowledge Store

The knowledge store persists learned context about a repository at `.majordomo/knowledge.json`. It tracks observations, suggestions, and resolved items across analysis runs.

```go
type Entry struct {
    Kind      string    // "observation", "suggestion", "resolved", "note"
    Topic     string    // "docs", "tests", "ci", "deps", "structure"
    Summary   string    // one-line human readable
    Details   string    // optional extended info
    Source    string    // "scan", "llm", "user"
    Resolved  bool
    Tags      []string
}

type Store struct {
    Entries    []Entry
    LastScan   time.Time
    LastReport json.RawMessage  // most recent scan.Report
}
```

**Methods:**

- `Open(repoRoot)` — opens or creates `.majordomo/knowledge.json`
- `Add(entry)` — adds entry, deduplicates by topic+summary
- `Resolve(id)` — marks a suggestion as done
- `Lookup(topic, kind)` — filter entries by topic and/or kind
- `OpenSuggestions()` — returns unresolved suggestions
- `ForLLM()` — formats all entries as LLM context string
- `Stats()` — returns (total, open, resolved) counts

**Topics:** `ci`, `docs`, `tests`, `deps`, `structure`

The store migrates legacy format (bare array) automatically on load. Entries are never deleted by the tool — only explicitly via `/forget` or user action.

## Markdown Rendering

The `mdrender` package provides terminal-optimized markdown rendering via [Glamour](https://github.com/charmbracelet/glamour).

**Key functions:**

- `NewRenderer(wordWrap)` — creates a `*glamour.TermRenderer` with configured width
- `TermWidth(fallback)` — detects terminal width, falls back to given value
- `IndentEachLine(prefix, s)` — indents each line for chat gutter alignment

**Terminal detection:**

The renderer auto-selects light or dark theme without using OSC queries that conflict with Bubble Tea. It uses `COLORFGBG` environment variable — background color index 7–15 indicates light terminal.

**Inline code style:** Backtick-delimited code is enhanced with bold styling for clarity in the TUI.

## TUI Interface

The TUI provides an interactive terminal interface built with [Bubble Tea](https://github.com/charmbracelet/bubbletea). It shares the same command registry as the CLI but renders output in a chat-style interface.

```
┌─────────────────────────────────────────────────────┐
│ majordomo                                          │
├─────────────────────────────────────────────────────┤
│ ❯ /analyze .                                       │
│   Running /analyze...                              │
│   Analyzing...                                     │
│   ## Analysis Complete                            │
│   Score: 72/100                                    │
│                                                     │
│ ❯ _                                                │
└─────────────────────────────────────────────────────┘
```

**Architecture:**

```
┌─────────────────────────────────────────────────────────┐
│                        App                              │
│  (mode: Chat | Config)                                  │
│         │                         │                     │
│         ▼                         ▼                     │
│    ┌─────────┐            ┌───────────────┐             │
│    │  Chat   │            │ ConfigEditor  │             │
│    │ (chat)  │            │   (form)      │             │
│    └────┬────┘            └───────────────┘             │
│         │                                               │
│         ▼                                               │
│  ┌─────────────────────────────────────────┐           │
│  │           commands.Registry              │           │
│  │   (shared between CLI and TUI)          │           │
│  └─────────────────────────────────────────┘           │
│         │                                               │
│         ▼                                               │
│  ┌─────────────────────────────────────────┐           │
│  │              Sink interface              │           │
│  │   Print | PrintMarkdown | Status | Error │           │
│  └─────────────────────────────────────────┘           │
│         │                       │                       │
│         ▼                       ▼                       │
│  ┌─────────────┐        ┌─────────────┐                │
│  │ StreamSink  │        │  CLISink    │                │
│  │ (TUI only)  │        │  (stdout)   │                │
│  └─────────────┘        └─────────────┘                │
└─────────────────────────────────────────────────────────┘
```

**Components:**

- **App** (`app.go`): Root model managing mode switching between Chat and ConfigEditor using Bubble Tea's Elm architecture
- **Chat** (`chat.go`): Interactive chat interface with command input, markdown rendering, autocomplete, command history (↑/↓), and cancellation (Ctrl+C/Esc)
- **ConfigEditor** (`config.go`): Form-based configuration editor for server URL, LLM provider, model, and endpoint
- **Sink interface** (`sink.go`): Abstracts output rendering; `StreamSink` sends messages to the TUI event loop, `CLISink` writes directly to stdout

**Command Execution:**

Commands are invoked via `/command` syntax. The TUI:
1. Parses input against the shared `commands.Registry`
2. Spawns a goroutine to run the command with a cancellable context
3. Writes output to the appropriate `Sink` implementation
4. Handles Ctrl+C/Esc cancellation by calling the context cancel function

**Sinks:**

The `Sink` interface enables commands to run identically in both CLI and TUI modes:

| Method        | StreamSink (TUI)              | CLISink (CLI)           |
|---------------|-------------------------------|-------------------------|
| `Print`       | Appends chat message          | Writes to stdout        |
| `PrintMarkdown` | Renders markdown inline      | Glamour-rendered stdout |
| `Status`      | Shows spinner text            | Writes to stderr        |
| `Error`       | Styled error message          | Writes "error:" to stderr |

The CLI (`majordomo analyze .`) uses `CLISink` for direct stdout output. The TUI (`majordomo`) uses `StreamSink` to send messages through Bubble Tea's event loop, keeping the UI responsive during long-running commands.

## MCP Server

Majordomo can run as an MCP (Model Context Protocol) server, exposing its tools to AI assistants for repository analysis.

```bash
majordomo mcp
```

**Transport:** Stdio-based JSON-RPC communication (stdin/stdout). Compatible with Claude Code, Cursor, and other MCP clients.

**Tools:**

| Tool       | Description                                      |
|------------|--------------------------------------------------|
| `analyze`  | Scan and grade repository, return structured results |
| `knowledge`| Show learned observations and suggestions         |
| `status`   | Show running jobs                                |
| `setup`    | Initialize majordomo for a repository            |

**Usage example:**

```json
// List tools
{"jsonrpc": "2.0", "method": "tools/list", "id": 1}

// Call analyze
{"jsonrpc": "2.0", "method": "tools/call", "params": {"name": "analyze", "arguments": {"path": ".", "no_llm": true}}, "id": 2}
```

**Tool output:** Commands run via `exec.Command` with JSON output, then parsed and formatted as readable text for the agent.

## Native Tool Calling (Function Calling)

Majordomo supports native LLM tool calling, allowing AI assistants to directly invoke commands during chat interactions. This is faster and more efficient than the MCP approach since tools execute in-process without subprocess overhead.

### Architecture

```
User Question
    ↓
Chat Command (chat_with_tools.go)
    ↓
Tool Bridge (toolbridge.go)
    ├─▶ Convert commands → OpenAI function schemas
    └─▶ Register execution handlers
    ↓
LLM Client (tools.go)
    ├─▶ Stream chat with tools available
    └─▶ Parse tool_calls from SSE response
    ↓
[Tool requested?]
    ├─ No  → Display response, done
    └─ Yes → Execute via Command Registry
              ↓
         Capture output (CaptureSink)
              ↓
         Add tool result to conversation
              ↓
         Loop back to LLM (max 5 iterations)
```

### Key Files

- **`internal/llm/tools.go`**: Extends LLM client with `ChatWithTools()` method supporting OpenAI function calling format
- **`internal/commands/toolbridge.go`**: Converts Command definitions to tool schemas and handles execution
- **`internal/commands/chat_with_tools.go`**: Chat command with tool calling loop

### Tool Schema Generation

Commands are automatically converted to LLM tools:

```go
// Command definition
Command{
  Name: "analyze",
  Description: "Scan the repo and grade it",
  Args: []Arg{
    {Name: "path", Description: "Repo path", Default: "."},
    {Name: "json", Description: "Output JSON", IsFlag: true},
  },
}

// Becomes tool schema
Tool{
  Type: "function",
  Function: {
    Name: "analyze",
    Description: "Scan the repo and grade it",
    Parameters: {
      "type": "object",
      "properties": {
        "path": {"type": "string", "description": "Repo path", "default": "."},
        "json": {"type": "boolean", "description": "Output JSON"}
      }
    }
  }
}
```

### Execution Flow

1. **User asks question**: "How many tests do I have?"
2. **LLM receives**: System prompt + user message + available tools
3. **LLM decides**: "I should call the analyze tool to check"
4. **Response includes**: `tool_calls: [{name: "analyze", arguments: "{}"}]`
5. **Bridge executes**: Runs analyze command via registry
6. **Output captured**: CaptureSink collects all sink output
7. **Result sent back**: Added as `role: "tool"` message
8. **LLM responds**: "You have 127 tests covering 78% of code"

### Model Requirements

Tool calling requires models that support the OpenAI function calling format. Not all models return `tool_calls` properly.

**Tested & Working:**
- qwen3:8b (recommended)
- qwen2.5-coder:32b
- deepseek-r1:8b

**Limited Support:**
- llama3.2 (returns tool calls as plain text, not structured)
- Small models < 7B parameters

### Performance

**In-process execution** means tools run with zero subprocess overhead:
- Command parsing: < 1ms
- Tool execution: Same as direct command invocation
- No serialization between processes
- Shared memory for knowledge store access

Compared to MCP (subprocess per tool call), native tool calling is **10-100x faster** for simple operations like status checks and knowledge queries.

### Available Tools

All non-hidden commands are exposed as tools:
- `analyze` - Repository scanning and grading
- `knowledge` - Query knowledge base
- `status` - Check running jobs
- `setup` - Initialize repository
- `resolve` - Mark issues as resolved
- `forget` - Remove knowledge entries

Hidden commands (help, quit, clear) and the chat command itself are excluded to prevent recursion.

### Safety & Limits

- **Max iterations**: 5 (prevents infinite tool calling loops)
- **Context cancellation**: User can Ctrl+C to abort at any point
- **Same permissions**: Tools run with same privileges as CLI user
- **No approval prompts**: Tools execute automatically (future enhancement)

### Example Interaction

```
User: What should I improve first?

LLM: [Calls knowledge tool with open_only=true]
Tool Result: "2 open suggestions: 1) Add pre-commit hooks 2) Increase test coverage"

LLM: [Calls analyze tool to get current metrics]
Tool Result: "Grade: B (78%). Tests: 45% coverage. No pre-commit hooks."

LLM Response: "Based on the analysis, I recommend starting with:
1. Add pre-commit hooks (currently missing)
2. Increase test coverage from 45% to 70%
These will raise your grade from B to A-."
```

### Extension

To add a new tool:
1. Register a command in `builtins.go`
2. It automatically becomes available as a tool
3. No additional tool definition needed

The toolbridge handles schema generation, parameter mapping, and execution automatically.
