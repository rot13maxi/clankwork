#!/bin/bash
# Pre-push test guard — blocks git push if the test suite is failing.
# Reads the Bash tool input from stdin (JSON) and checks if it's a git push.

INPUT=$(cat)

# Only intercept git push commands
IS_PUSH=$(echo "$INPUT" | python3 -c "
import sys, json
d = json.load(sys.stdin)
cmd = d.get('command', '')
print('yes' if 'git push' in cmd else 'no')
" 2>/dev/null)

if [[ "$IS_PUSH" != "yes" ]]; then
  exit 0
fi

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0

echo "» Running test suite before push..." >&2
cd "$ROOT/agent-harness"
if ! bun run test 2>&1; then
  echo "" >&2
  echo "✗ Tests failed — push blocked. Fix the failures above and try again." >&2
  exit 1
fi

echo "» Tests passed. Proceeding with push." >&2
