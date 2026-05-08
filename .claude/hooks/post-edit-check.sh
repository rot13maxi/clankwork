#!/bin/bash
# Post-edit typecheck hook — runs after every file write in agent-harness/
# Reads the written file path from stdin (JSON tool input), then runs tsc.

INPUT=$(cat)

# Extract the file path from the tool input JSON (Claude Code PostToolUse format)
FILE_PATH=$(echo "$INPUT" | python3 -c "
import sys, json
d = json.load(sys.stdin)
inp = d.get('tool_input', d)
print(inp.get('file_path', inp.get('path', '')))
" 2>/dev/null)

# Only run if editing files inside agent-harness/
if [[ "$FILE_PATH" != *"agent-harness"* ]]; then
  exit 0
fi

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
cd "$ROOT/agent-harness"

if [ ! -f "tsconfig.json" ]; then
  exit 0
fi

echo "» tsc --noEmit..." >&2
bunx tsc --noEmit 2>&1
