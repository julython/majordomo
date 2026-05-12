# Majordomo

Majordomo is an AI-powered repo health grading and PR triage tool for open source maintainers. It runs as a single Go binary on the maintainer's machine, analyzes local repositories using parallel goroutines, and uses a local LLM for intelligent insights. It optionally connects to a central server for dashboard access, webhook-driven PR triage, and team features.

## Features

- 🔍 **Repository Analysis** - Fast Go-based scanning of code structure, tests, docs, and CI
- 🤖 **AI Chat with Tool Calling** - Ask questions and the LLM can execute commands to answer accurately
- 📊 **Health Grading** - Automatic scoring across multiple dimensions
- 💬 **Interactive TUI** - Beautiful terminal interface with streaming responses
- 🔌 **MCP Support** - Model Context Protocol server for integration with other tools
- 🚀 **Fast & Local** - All processing in Go, LLM runs on your machine

## Getting started

1. Install Go (1.24+)
2. Install [Ollama](https://ollama.com) for local LLM support
3. Pull down this repo
4. Run `make setup`

### Quick Start

```bash
# Build the binary
go build -o majordomo ./cmd/majordomo

# Run interactive TUI
./majordomo

# Or use CLI mode
./majordomo analyze .
./majordomo chat "How many tests do I have?"
```

## Tool Calling

Majordomo supports LLM tool calling for intelligent command execution. See [docs/TOOL_CALLING.md](docs/TOOL_CALLING.md) for details.

**Recommended LLM models for tool calling:**
- qwen3:8b (best balance)
- qwen2.5-coder:32b (code-focused)
- deepseek-r1:8b (strong reasoning)

```bash
# Install a compatible model
ollama pull qwen3:8b

# Update config to use it
./majordomo config
```
