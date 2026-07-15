# cel-rule-skill

> **Status: upstreaming to compliance-operator.** celctl and the skill are being moved to
> [`ComplianceAsCode/compliance-operator` `cmd/celctl`](https://github.com/ComplianceAsCode/compliance-operator/pull/1306)
> so they reuse the operator's scanner engine directly. Once that merges, install with
> `go install github.com/ComplianceAsCode/compliance-operator/cmd/celctl@latest` and this
> repo becomes historical. Until then, this repo remains the released home (`celctl/v0.1.4`).

A [Claude Code](https://claude.com/claude-code) skill **and** a self-contained CLI (`celctl`)
for authoring, validating, and running **Kubernetes CEL compliance rules** — with
**no server required**.

This replaces the [`cel-rpc-server`](https://github.com/Vincent056/cel-rpc-server) MCP
server. The work is done by a small local Go utility, `celctl`, that evaluates CEL with
the same engine the Compliance Operator scanner uses (cel-go), and runs rules against a
live cluster via `kubectl`. Rules themselves live in the
ComplianceAsCode/content repo (`applications/<app>/<rule>/cel/shared.yml`).

## Install

```bash
go install github.com/Vincent056/cel-rule-skill/celctl@latest
```

The Claude Code skill lives in `celctl/skill/` as plain files. Install it by copying (or
symlinking) the directory into your Claude Code skills dir — you or Claude can do this:

```bash
mkdir -p ~/.claude/skills
cp -r celctl/skill ~/.claude/skills/cel-rule          # from a clone
# or: ln -s "$PWD/celctl/skill" ~/.claude/skills/cel-rule   (tracks your checkout)
```

Restart Claude Code to pick the skill up.

Live-cluster commands additionally need `kubectl` configured for the target cluster.
Local `verify`/`eval`/`cac test` need nothing else — no server, no container, no AI keys.

### Updating

```bash
go install github.com/Vincent056/cel-rule-skill/celctl@latest   # the tool
git pull    # the skill, if you symlinked a clone; re-copy otherwise
```

## celctl — the utility

```
celctl cac      lint|scaffold|test|live <rule-dir> ComplianceAsCode/content rules (primary)
celctl eval     --expr '<cel>' --data v=v.json    Evaluate once, print the boolean
celctl verify   --expr '<cel>' --test cases.json  Run ad-hoc test cases
celctl live     --expr '<cel>' --input pods=v1/pods:default
celctl discover                                   kubectl api-resources
celctl samples  <resource> [-n ns] [--max N]      Sample objects to model test data on
```

### Validating ComplianceAsCode/content (cac-content) rules

`celctl cac` reads the shipping `applications/<app>/<rule>/cel/shared.yml` format and binds
inputs **exactly like the Compliance Operator scanner** (single object when `resource_name`
is set, else a `{items:[...]}` List wrapper). So a rule that passes here behaves the same in
the operator.

```bash
# smoke-test every rule in an app — catches the common "iterate a list without .items" bug
for d in applications/openshift-virtualization/*/; do celctl cac lint "$d"; done

# scaffold fixtures from REAL cluster objects (sanitized, provenance-stamped)
celctl cac scaffold <rule-dir> --from-cluster

# unit-test with fixtures (no cluster)
celctl cac test <rule-dir> --cases cases.yaml

# evaluate against a real cluster
celctl cac live <rule-dir>
```

The CEL environment matches the operator's `compliance-sdk` scanner exactly: standard
library plus the custom `parseJSON` / `parseYAML` functions, every input declared dynamic.

## Usage

Ask Claude, e.g.:

- "Write a CEL rule that every pod runs as non-root, and validate it."
- "Check the live cluster: do all deployments in `production` have ≥2 replicas?"
- "Scaffold fixtures for this rule from the cluster and run them."

Claude invokes the `cel-rule` skill and drives `celctl` through the
scaffold → lint → test → live loop.

Or use the CLI directly:

```bash
celctl cac test applications/openshift-virtualization/kubevirt-nonroot-feature-gate-is-enabled
celctl live --expr 'deployments.items.all(d, d.spec.replicas >= 2)' \
            --input deployments=apps/v1/deployments:production
```

## Repo layout

```
celctl/                        # the utility (replaces the MCP server)
  main.go, cac.go              #   verify / eval / live / discover / samples / cac
  scaffold.go                  #   fixture scaffolding
  skill/                       # the Claude Code skill (install: copy/symlink, see Install)
    SKILL.md                   #   workflows for create / validate / run
    references/
      cel-cookbook.md          #   CEL syntax patterns (verified against cel-go)
      examples.md              #   rule-file format + every celctl command
scripts/
  build.sh                     # build (and optionally install) celctl from a clone
```

## Notes

- CEL inputs are List-wrapped: iterate with `<var>.items.all(x, ...)` / `.exists(...)`.
  See [references/cel-cookbook.md](celctl/skill/references/cel-cookbook.md).
- Migrating from cel-rpc-server? The rule library is gone by design — port rules to
  cac-content format (`cel/shared.yml` + `cel/tests/`) and use `celctl cac …`.
