# Tool Calling in Majordomo

Majordomo supports LLM tool calling (function calling), allowing the AI to directly execute commands to answer your questions more accurately.

## What is Tool Calling?

When you ask a question in chat mode, the LLM can now:
1. Analyze your question
2. Decide which commands to run (analyze, knowledge, status, etc.)
3. Execute those commands automatically
4. Use the results to give you an informed answer

For example:
- **You**: "What's the test coverage in this repo?"
- **AI**: *Calls `analyze` tool* → "Based on the analysis, you have 127 tests covering 78% of your source code..."

## Model Requirements

Tool calling requires a model that supports the OpenAI function calling format. Not all models support this properly.

### ✅ Recommended Models (Tested & Working)

- **qwen3:8b** - Best balance of speed and accuracy for tool calling
- **qwen2.5-coder:32b** - Excellent for code-related tasks (requires more RAM)
- **deepseek-r1:8b** - Good reasoning capabilities with tool support

### ⚠️ Limited Support

- **llama3.2** - Returns tool calls as plain text instead of structured format (default model, doesn't work with tools)
- **llama3.1** - Similar limitations to 3.2
- **qwen2.5-coder:0.5b** - Too small for reliable tool calling

### Configuration

To use a compatible model with Ollama:

```bash
# Pull a compatible model
ollama pull qwen3:8b

# Update your majordomo config
# Edit ~/.config/majordomo/config.toml:
[llm]
provider = "ollama"
model = "qwen3:8b"
url = "http://localhost:11434"
```

Or run the config command:
```bash
./majordomo config
```

## How It Works

### Architecture

1. **Command → Tool Bridge**: Each majordomo command (analyze, knowledge, etc.) is automatically exposed as an LLM tool with:
   - Name and description
   - Parameter schema derived from command args
   - Execution handler

2. **Chat Loop**: When you ask a question:
   ```
   User Question → LLM → Tool Call(s) → Execute Commands → LLM → Final Answer
   ```

3. **Fast Execution**: Tools run directly in Go - no subprocess overhead for most operations

### Available Tools

The LLM can call any non-hidden command:
- `analyze` - Scan and grade the repository
- `knowledge` - Query the knowledge base
- `status` - Check running jobs
- `setup` - Initialize the repo
- `resolve` - Mark issues as resolved
- `forget` - Remove knowledge entries

## Example Interactions

### Basic Query
```
You: How many files are in this project?
AI: [Calls analyze tool]
AI: This project contains 89 files: 67 source files, 15 tests, and 7 configuration files.
```

### Multi-Step Reasoning
```
You: What should I improve first?
AI: [Calls knowledge tool to check existing suggestions]
AI: [Calls analyze tool if knowledge is stale]
AI: Based on the analysis, I recommend starting with:
    1. Add pre-commit hooks (currently missing)
    2. Increase test coverage from 45% to 70%
    3. Add API documentation
```

### Follow-up Context
```
You: Mark the first one as done
AI: [Calls resolve tool with the pre-commit hook suggestion ID]
AI: ✓ Marked "Add pre-commit hooks" as resolved. Great job!
```

## Testing Tool Calling

```bash
# Build the project
go build -o majordomo ./cmd/majordomo

# Test with a model that supports tools
echo "What is this project about?" | ./majordomo

# Or use interactive mode
./majordomo
# Then type your questions
```

## Troubleshooting

### Tools aren't being called
- Check that you're using a compatible model (qwen3:8b recommended)
- Verify the model is running: `ollama list`
- Check logs for "Tool calling not supported" errors

### Tools execute but no final response
- This usually indicates the model is struggling with the format
- Try a larger model or one from the recommended list
- Check if you're hitting the max iteration limit (5 by default)

### Performance issues
- Smaller models (< 7B parameters) may be too slow for reliable tool calling
- Consider using a quantized larger model instead
- The qwen3:8b Q4_K_M quantization is a good sweet spot

## Implementation Details

For developers interested in the implementation:

- **LLM Client Extension**: `internal/llm/tools.go` adds `ChatWithTools` method
- **Tool Bridge**: `internal/commands/toolbridge.go` converts commands to tools
- **Chat Integration**: `internal/commands/chat_with_tools.go` implements the chat loop
- **Streaming**: Tool calls and responses stream in real-time for better UX

The system uses:
- OpenAI-compatible function calling format
- Streaming SSE responses for real-time feedback
- Automatic retry loop (max 5 iterations) for multi-step reasoning
- Zero-copy execution of tools (no subprocess overhead)

## Future Enhancements

Planned improvements:
- [ ] Parallel tool execution
- [ ] Tool execution history/undo
- [ ] Custom tool definitions via plugins
- [ ] Approval prompts for destructive operations
- [ ] Token usage tracking and limits
