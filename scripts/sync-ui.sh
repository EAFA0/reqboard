#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cp "$ROOT/vscode-ext/media/board.css" "$ROOT/cli/internal/webui/assets/board.css"
cp "$ROOT/vscode-ext/media/board.js" "$ROOT/cli/internal/webui/assets/board.js"
echo "Synced board.css and board.js -> cli/internal/webui/assets/"
