---
name: cel-rule
description: Author, validate, run, and manage Kubernetes CEL compliance rules with the local celctl utility — no server required. Use when the user wants to write a CEL expression/rule, test it against sample data or a live cluster, or add/list/test/remove rules in a local rule library.
---

# CEL Rule

End-to-end helper for **Common Expression Language (CEL)** rules that check Kubernetes
resources. It drives **`celctl`**, a self-contained CLI in this repo that evaluates CEL
locally with cel-go (the same engine the old `cel-rpc-server` used), runs rules against a
live cluster via `kubectl`, and manages a file-based rule library.

> **This replaces the cel-rpc-server MCP server.** No server, no container, no AI keys.
> Everything is a local `celctl` invocation.

> **You do not evaluate CEL by hand.** Always round-trip every expression through `celctl`
> (`verify`/`eval`/`live`). Hand-reasoning about CEL semantics is unreliable; `celctl` is
> the source of truth.

## Prerequisite: build celctl once

The skill assumes a `celctl` binary exists. If `celctl` (or `$CELCTL`) isn't on PATH,
build it from this repo:

```bash
cd <this-repo>/celctl && go build -o celctl .      # produces ./celctl
# optional: install it
cp celctl ~/.local/bin/        # or anywhere on PATH
```

Throughout, invoke it as `celctl` if installed, else `<repo>/celctl/celctl`.
Live-cluster commands additionally require a working `kubectl` (current context = the
cluster you want to check).

## celctl command map (replaces the old MCP tools)

| Old MCP tool | celctl command |
|---|---|
| `verify_cel_with_tests` | `celctl verify --rule r.json` / `celctl verify --expr '<cel>' --test cases.json` |
| (ad-hoc single eval) | `celctl eval --expr '<cel>' --data var=data.json` |
| `verify_cel_live_resources` | `celctl live --rule r.json` / `celctl live --expr '<cel>' --input name=[group/]version/resource[:ns]` |
| `discover_resource_types` | `celctl discover` |
| `get_resource_samples` | `celctl samples <resource> [-n ns] [--max N]` |
| `add_rule` | `celctl rule add --file r.json` (auto-validates test cases before saving) |
| `list_rules` | `celctl rule list [--category c] [--tag t] [--search text]` |
| `get_rule` | `celctl rule get <id>` |
| `test_rule` | `celctl rule test <id> [--mode test_cases\|live]` |
| `remove_rule` | `celctl rule remove <id>` |

The rule-library directory defaults to `./rules-library` (override with `--dir`). The rule
JSON format is **compatible with cel-rpc-server's** files, so existing libraries work as-is.

---

## Workflow A — Create & validate a rule (no cluster)

The default loop. Iterate here until the expression is correct, *then* optionally save it
or run it live.

1. **Clarify intent.** What resource(s)? What makes a resource compliant? Pin the
   namespace/scope and the exact field path.

2. **Pick the input shape.** Each input becomes a CEL variable bound to a Kubernetes
   **List** (`{"items":[...]}`). Decide the variable name and `version`/`resource`
   (`group` only for non-core types). Unsure of the real layout? Run
   `celctl samples <resource> -n <ns>` against a cluster and model your test data on it.

3. **Write the expression** per [references/cel-cookbook.md](references/cel-cookbook.md).
   The critical rule: data is List-wrapped, so iterate with `<var>.items.all(x, ...)` /
   `.exists(...)`.

4. **Write a rule file** (or a bare test-cases file) and run `celctl verify`. Include a
   happy-path case (`expected_result: true`), a failure case (`false`), and an empty-list
   edge case. Full file format: [references/examples.md](references/examples.md).

   ```bash
   celctl verify --rule myrule.json
   # or ad-hoc:
   celctl verify --expr 'pods.items.all(p, has(p.spec.securityContext) && p.spec.securityContext.runAsNonRoot == true)' \
                 --test cases.json
   ```

5. **Read the result.** `celctl` prints per-case ✅/❌ and `N/M passed`, exiting non-zero
   if any case fails. If a case is wrong, decide whether the *expression* or the
   *expected_result* is mistaken, fix, and re-run. Mind the empty-list case: `all()` over
   `[]` is `true`, `exists()` over `[]` is `false` — make it match your intent.

6. Report the final expression, inputs, and the passing test matrix to the user.

## Workflow B — Run against a live cluster

Use when the user wants to know whether the *actual* cluster is compliant. Requires
`kubectl` pointed at the target cluster.

1. Optionally confirm the resource exists / is populated: `celctl discover` or
   `celctl samples <resource> -n <ns>`.
2. Run live. The expression must be List-form (`<var>.items.all(...)`) because each input
   is fetched as a List via `kubectl get -o json`.

   ```bash
   celctl live --rule myrule.json
   # or ad-hoc:
   celctl live --expr 'deployments.items.all(d, d.spec.replicas >= 2)' \
               --input deployments=apps/v1/deployments:production
   ```
   Input spec is `name=[group/]version/resource[:namespace]`; omit `:namespace` to query
   all namespaces (`-A`).
3. `celctl` prints how many items it fetched per input and a final ✅ PASS / ❌ FAIL
   (exit 0/1). If the result is surprising, `celctl samples` the resource and re-validate
   the expression in Workflow A against that real data before blaming the cluster.

## Workflow C — Manage the rule library

```bash
celctl rule list --dir ./rules-library                 # browse / filter
celctl rule list --category security --search network
celctl rule get <id>                                   # full JSON
celctl rule add --file myrule.json                     # validates test cases, then saves
celctl rule test <id>                                  # replay stored test cases
celctl rule test <id> --mode live                      # run saved rule vs cluster
celctl rule remove <id>
```

- `rule add` **refuses to save** a rule whose test cases don't pass — so only validated
  rules enter the library. Always author with test cases.
- Before adding, `celctl rule list --search <keyword>` to check for a near-duplicate and
  edit that file instead of creating a second copy.

---

## Guardrails

- Never claim a CEL expression "works" without a passing `celctl verify`/`live` result to
  back it up. Quote the `N/M passed` line.
- Test data must be a List: `{"items":[...]}`. A bare object (no `items`) will not iterate.
- Guard optional fields with `has(x.field)` before dereferencing — a missing map key or
  field raises an eval error (surfaced as ❌ for that case).
- For `live`, the expression must be List-form; everything comes back wrapped in `items`.
- Only `celctl rule add` rules that have already passed their test cases (it enforces this).

See [references/cel-cookbook.md](references/cel-cookbook.md) for CEL syntax patterns and
[references/examples.md](references/examples.md) for the rule-file format and every
`celctl` command with example payloads.
