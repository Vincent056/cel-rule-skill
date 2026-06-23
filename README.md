# cel-rule-skill

A [Claude Code](https://claude.com/claude-code) skill **and** a self-contained CLI for
authoring, validating, running, and managing **Kubernetes CEL compliance rules** — with
**no server required**.

This replaces the [`cel-rpc-server`](https://github.com/Vincent056/cel-rpc-server) MCP
server. Instead of running a container and talking to it over MCP, the work is done by a
small local Go utility, `celctl`, that evaluates CEL with the same engine (cel-go), runs
rules against a live cluster via `kubectl`, and manages a file-based rule library. The
rule JSON format is **compatible with cel-rpc-server's**, so existing libraries work as-is.

## What's in here

```
celctl/                        # the utility (replaces the MCP server)
  main.go                      #   verify / eval / live / rule / discover / samples
  go.mod, go.sum
skills/cel-rule/
  SKILL.md                     # the skill: workflows for create / validate / run / manage
  references/
    cel-cookbook.md            #   CEL syntax patterns (verified against cel-go)
    examples.md                #   rule-file format + every celctl command
scripts/
  build.sh                    # build (and optionally install) celctl
```

## celctl — the utility

```
celctl verify   --rule rule.json                  Run a rule's test cases (no cluster)
celctl verify   --expr '<cel>' --test cases.json  Run ad-hoc test cases
celctl eval     --expr '<cel>' --data v=v.json    Evaluate once, print the boolean
celctl live     --rule rule.json                  Run against the live cluster (kubectl)
celctl live     --expr '<cel>' --input pods=v1/pods:default
celctl rule     list|get|add|test|remove [--dir ./rules-library]
celctl discover                                   kubectl api-resources
celctl samples  <resource> [-n ns] [--max N]      Sample objects to model test data on
```

How it maps to the old MCP tools:

| cel-rpc-server MCP tool | celctl command |
|---|---|
| `verify_cel_with_tests` | `celctl verify` |
| `verify_cel_live_resources` | `celctl live` |
| `discover_resource_types` | `celctl discover` |
| `get_resource_samples` | `celctl samples` |
| `add_rule` / `list_rules` / `get_rule` / `test_rule` / `remove_rule` | `celctl rule add/list/get/test/remove` |

`celctl rule add` validates a rule's test cases and **refuses to save** if any fail, so
only verified rules enter the library.

## Setup

### 1. Build celctl (one-time, needs Go 1.21+)

```bash
./scripts/build.sh --install     # builds and copies to ~/.local/bin
# or just: cd celctl && go build -o celctl .
```

Live-cluster commands additionally need `kubectl` configured for the target cluster.
Local `verify`/`eval` need nothing else — no server, no container, no cluster, no AI keys.

### 2. Install the skill

Point Claude Code at the skill — symlink it into your skills dir:

```bash
ln -s "$PWD/skills/cel-rule" ~/.claude/skills/cel-rule
```

…or add this repo as a plugin/marketplace source. Restart Claude Code to pick it up.

## Usage

Ask Claude, e.g.:

- "Write a CEL rule that every pod runs as non-root, and validate it."
- "Check the live cluster: do all deployments in `production` have ≥2 replicas?"
- "Save that rule to the library tagged CIS 5.3.2, high severity."
- "List the security rules and re-run their tests."

Claude invokes the `cel-rule` skill and drives `celctl` through the
create → validate → (run) → save loop.

Or use the CLI directly:

```bash
celctl verify --rule rules-library/etcd.json
celctl live --expr 'deployments.items.all(d, d.spec.replicas >= 2)' \
            --input deployments=apps/v1/deployments:production
```

## Notes

- The rule library defaults to `./rules-library/` (override with `--dir`).
- CEL inputs are List-wrapped: iterate with `<var>.items.all(x, ...)` / `.exists(...)`.
  See [references/cel-cookbook.md](skills/cel-rule/references/cel-cookbook.md).
- Migrating from cel-rpc-server? Copy its `rules-library/` here and run
  `celctl rule list` / `celctl rule test <id>` — the JSON format is the same.
