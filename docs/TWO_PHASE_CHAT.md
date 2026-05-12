# Two-Phase Chat Architecture

## Overview

The chat command now uses a two-phase approach to generate better implementation plans:

1. **Pre-Planning Phase**: The LLM acts as a prompt engineer to identify what code context it needs
2. **Planning Phase**: With full context, the LLM creates detailed implementation steps

## Architecture

### Phase 1: Pre-Planning (Prompt Engineering)

**Goal**: Identify what code needs to be examined before creating a plan.

**Process**:
1. Index the repository
2. Build pre-planning prompt with:
   - Repository skeleton (file tree + symbol signatures)
   - User's request
   - Instructions to act as a prompt engineer
3. Send to LLM
4. Parse response to extract:
   - Initial approach summary
   - List of symbols to examine (functions/classes/types)
   - List of files to read

**Prompt**: See `BuildPrePlanningPrompt()` in [preplanner.go](internal/repomap/planner/preplanner.go)

**Output**: JSON object with:
```json
{
  "summary": "brief description of approach",
  "symbols": ["FunctionName", "ClassName"],
  "files": ["path/to/file.go"]
}
```

### Phase 2: Context Gathering

**Goal**: Fetch the actual source code for requested symbols and files.

**Process**:
1. Read each requested file in full
2. Extract body of each requested symbol
3. Build context map with all the code

**Implementation**: See `GatherContext()` in [preplanner.go](internal/repomap/planner/preplanner.go)

### Phase 3: Enhanced Planning

**Goal**: Create detailed implementation plan with full context.

**Process**:
1. Build enhanced planning prompt with:
   - Initial approach summary
   - Full source code of requested symbols/files
   - Repository skeleton
   - User's request
   - Instructions to create detailed steps
2. Send to LLM with tool support
3. Parse response into structured plan

**Prompt**: See `BuildEnhancedPlanningPrompt()` in [preplanner.go](internal/repomap/planner/preplanner.go)

**Output**: JSON plan with specific edit steps:
```json
{
  "summary": "one-line approach description",
  "steps": [
    {
      "action": "modify",
      "target": "HandleRequest",
      "file": "api/handler.go",
      "task": "Add rate limiting check at start of function..."
    }
  ]
}
```

### Phase 4: Tool-Assisted Execution

**Goal**: Allow LLM to use tools during planning if needed.

The planning phase runs in a chat loop with tools available:
- `symbol_content`: Read specific symbols
- Other registered commands

This allows the LLM to request additional context if the pre-planning phase missed something.

### Phase 5: Plan Execution

**Goal**: Execute the generated plan with user confirmation.

**Process**:
1. Show plan summary to user
2. Request confirmation
3. Execute each step in sequence:
   - `modify`: Replace symbol body
   - `add`: Insert new code after symbol
   - `create`: Write new file
   - `delete`: Remove symbol
   - `run`: Execute shell command (tests, build)

## Benefits

### 1. Better Context Selection
The LLM explicitly decides what code it needs to see, rather than relying on heuristics.

### 2. More Detailed Instructions
With full source code available, the LLM can create specific, actionable steps rather than vague instructions.

### 3. Reduced Token Waste
Only relevant code is included in the planning prompt, not entire files or unnecessary context.

### 4. Explicit Reasoning
The pre-planning summary shows the LLM's initial approach, making it easier to debug when plans are wrong.

### 5. Extensible
The tool loop in phase 4 allows the LLM to request additional context if needed.

## File Structure

```
internal/
├── commands/
│   ├── builtins.go          # chatCommand() orchestrates the 2 phases
│   └── toolbridge.go         # Converts commands to LLM tools
└── repomap/
    └── planner/
        ├── planner.go        # Original planning logic
        ├── plan.go           # Plan data structures
        └── preplanner.go     # NEW: Pre-planning phase logic
```

## Example Flow

**User Request**: "Add rate limiting to the API handler"

**Phase 1 - Pre-Planning**:
```
LLM examines skeleton → identifies:
- symbols: ["HandleRequest", "NewServer"]
- files: ["api/middleware.go"]
- summary: "Add rate limiter middleware and integrate in HandleRequest"
```

**Phase 2 - Context Gathering**:
```
Reads:
- Full body of HandleRequest function
- Full body of NewServer function  
- Complete api/middleware.go file
```

**Phase 3 - Planning**:
```
LLM creates detailed plan:
1. modify NewServer: Add rate limiter initialization
2. create api/ratelimit.go: Implement RateLimiter type
3. modify HandleRequest: Add rate limit check
4. run: go test ./api/...
```

**Phase 4 - Execution**:
```
User confirms → executes each step → shows results
```

## Future Enhancements

1. **Caching**: Cache pre-planning responses for similar requests
2. **Learning**: Track which symbols were actually needed vs requested
3. **Dependency Analysis**: Auto-suggest additional symbols based on call graph
4. **Iterative Refinement**: Allow LLM to request more context mid-planning
5. **Confidence Scores**: Track how confident the LLM is about each step

## Testing

To test the new flow:

```bash
./majordomo
> /chat add a new endpoint to the API

# Watch for:
# 1. "Pre-planning: identifying required context..."
# 2. "📋 Pre-plan: <summary>"
# 3. "Examining N symbols: [...]"
# 4. "Reading N files: [...]"
# 5. "Creating detailed plan..."
# 6. Plan display with steps
```

## Notes

- The pre-planning LLM call does NOT have tools enabled (simpler, faster)
- The planning LLM call DOES have tools enabled (for additional context)
- Both phases use the same LLM model from config
- If pre-planning fails to parse, the system falls back to showing the raw response
- Context gathering is defensive: missing symbols/files are skipped, not errors
