# cel-rule-skill

A [Claude Code](https://claude.com/claude-code) skill for authoring, validating, running,
and managing **Kubernetes CEL compliance rules** — backed by the
[`cel-rpc-server`](https://github.com/Vincent056/cel-rpc-server) MCP server.

The skill turns the cel-rpc-server's MCP tools into an easy, guided workflow: describe the
rule you want, and Claude writes the CEL expression, validates it against sample data (and
optionally a live cluster) until it's correct, and can save it to a rule library.

## What's in here

```
skills/cel-rule/
  SKILL.md                     # the skill: workflows for create / validate / run / manage
  references/
    cel-cookbook.md            # CEL syntax patterns (verified against the engine)
    examples.md                # exact MCP tool-call payloads, copy-paste ready
scripts/
  start-server.sh             # launch the MCP server + register it with Claude Code
```

## How it works

`cel-rpc-server` exposes a CEL evaluation engine over the Model Context Protocol
(streamable HTTP at `http://localhost:8349/mcp`). The skill instructs Claude to round-trip
every expression through these MCP tools rather than reasoning about CEL by hand:

- `verify_cel_with_tests` — evaluate against inline sample data (no cluster needed)
- `verify_cel_live_resources` — evaluate against live cluster resources
- `discover_resource_types`, `count_resources`, `get_resource_samples` — cluster discovery
- `add_rule`, `list_rules`, `get_rule`, `test_rule`, `update_rule`, `remove_rule` — rule library

No AI API keys are needed — MCP mode is self-contained.

## Setup

### 1. Start the MCP server and register it

```bash
# mock mode (test-case validation only, no cluster):
./scripts/start-server.sh

# with live-cluster support:
KUBECONFIG=~/.kube/config ./scripts/start-server.sh
```

This runs the container and registers the `cel-validation` MCP server with Claude Code
(user scope). Verify with:

```bash
claude mcp get cel-validation     # expect: Status: ✔ Connected
```

(Manual alternative: `claude mcp add --scope user --transport http cel-validation http://localhost:8349/mcp`)

### 2. Install the skill

Point Claude Code at the skill. Either symlink it into your skills dir:

```bash
ln -s "$PWD/skills/cel-rule" ~/.claude/skills/cel-rule
```

…or, if you manage skills as a plugin/marketplace, add this repo as a source. Restart
Claude Code so it picks up the new skill.

## Usage

Just ask, e.g.:

- "Write a CEL rule that every pod runs as non-root, and validate it."
- "Check the live cluster: do all deployments in `production` have ≥2 replicas?"
- "Save that rule to the library tagged CIS 5.3.2, high severity."
- "List the security rules in the library and re-run their tests."

Claude will invoke the `cel-rule` skill and drive the MCP tools through the
create → validate → (run) → save loop.

## Notes

- Live-cluster checks require a kubeconfig mounted into the container (the start script
  handles this when `KUBECONFIG` is set). Without it, only `verify_cel_with_tests` works.
- The rule library persists to `./rules-library/` on the host (mounted into the container).
