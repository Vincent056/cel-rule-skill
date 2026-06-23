#!/usr/bin/env bash
# Start the cel-rpc-server MCP server and register it with Claude Code.
#
# Usage:
#   ./scripts/start-server.sh                # mock mode (no cluster) — test-case validation only
#   KUBECONFIG=~/.kube/config ./scripts/start-server.sh   # mount kubeconfig for live checks
#
# Requires: podman (or docker), and the `claude` CLI on PATH.
set -euo pipefail

PORT="${PORT:-8349}"
NAME="${NAME:-cel-rpc-server}"
IMAGE="${IMAGE:-ghcr.io/vincent056/cel-rpc-server}"
RULES_DIR="${RULES_DIR:-$PWD/rules-library}"
ENGINE="$(command -v podman || command -v docker)"

mkdir -p "$RULES_DIR"

ARGS=(run -d --name "$NAME" --replace -p "${PORT}:8349"
      -v "${RULES_DIR}:/home/celuser/app/rules-library:Z")

# Mount kubeconfig if one is set and exists — enables verify_cel_live_resources.
if [[ -n "${KUBECONFIG:-}" && -f "${KUBECONFIG}" ]]; then
  echo "Mounting kubeconfig from ${KUBECONFIG} (live-cluster checks enabled)"
  ARGS+=(-v "${KUBECONFIG}:/KUBECONFIG/kubeconfig:Z")
else
  echo "No KUBECONFIG mounted — running in mock mode (verify_cel_with_tests only)"
fi

echo "Starting ${NAME} on port ${PORT}..."
"$ENGINE" "${ARGS[@]}" "$IMAGE"

# Give it a moment, then register with Claude Code (idempotent-ish).
sleep 2
if command -v claude >/dev/null 2>&1; then
  claude mcp remove cel-validation -s user >/dev/null 2>&1 || true
  claude mcp add --scope user --transport http cel-validation "http://localhost:${PORT}/mcp"
  echo "Registered MCP server 'cel-validation'. Checking health..."
  claude mcp get cel-validation || true
else
  echo "claude CLI not found — register manually:"
  echo "  claude mcp add --scope user --transport http cel-validation http://localhost:${PORT}/mcp"
fi
