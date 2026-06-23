#!/usr/bin/env bash
# Build the celctl binary and (optionally) install it onto your PATH.
#
# Usage:
#   ./scripts/build.sh            # builds celctl/celctl
#   ./scripts/build.sh --install  # also copies it to ~/.local/bin
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT/celctl"

echo "Building celctl..."
go build -o celctl .
echo "Built: $REPO_ROOT/celctl/celctl"

if [[ "${1:-}" == "--install" ]]; then
  DEST="${INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$DEST"
  cp celctl "$DEST/celctl"
  echo "Installed: $DEST/celctl"
  command -v celctl >/dev/null 2>&1 || echo "note: ensure $DEST is on your PATH"
fi

echo
echo "Try it:"
echo "  $REPO_ROOT/celctl/celctl --help"
