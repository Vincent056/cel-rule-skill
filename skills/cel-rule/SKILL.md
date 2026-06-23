---
name: cel-rule
description: Author, validate, run, and manage Kubernetes CEL compliance rules through the cel-rpc-server MCP server. Use when the user wants to write a CEL expression/rule, test a CEL rule against sample data or a live cluster, or add/list/update/remove rules in the CEL rule library.
---

# CEL Rule

End-to-end helper for **Common Expression Language (CEL)** rules that check Kubernetes
resources. It drives the [`cel-rpc-server`](https://github.com/Vincent056/cel-rpc-server)
MCP server, which exposes the actual validation engine — you write the expression, the
server evaluates it (against test data or a real cluster) and reports pass/fail.

> **You do not evaluate CEL yourself.** Always round-trip every expression through the MCP
> tools below. Hand-reasoning about CEL semantics is unreliable; the server is the source
> of truth.

## Prerequisites — the MCP server must be reachable

These skills call MCP tools served by `cel-rpc-server` at `http://localhost:8349/mcp`.
Before using any tool, confirm the `cel-validation` MCP server is connected. If tools are
missing, set it up:

```bash
# 1. Start the server (mock mode = no cluster needed; good for test-case validation)
#    See scripts/start-server.sh in this repo for a one-shot launcher.
podman run -d --name cel-rpc-server -p 8349:8349 \
  -v ./rules-library:/home/celuser/app/rules-library:Z \
  --replace ghcr.io/vincent056/cel-rpc-server

# 2. Register it with Claude Code (user scope = all projects)
claude mcp add --scope user --transport http cel-validation http://localhost:8349/mcp

# 3. Verify
claude mcp get cel-validation     # expect: Status: ✔ Connected
```

For **live-cluster** checks the server also needs a kubeconfig — mount `~/.kube/config`
to `/KUBECONFIG/kubeconfig` or run `setup-kubeconfig` inside the container (see the
repo README). Without it, only `verify_cel_with_tests` and the test-case tools work;
live tools fall back to mock data.

No AI API keys are required for any of this — MCP mode is self-contained.

## Available MCP tools

| Tool | Purpose |
|------|---------|
| `verify_cel_with_tests` | Evaluate an expression against inline sample data. **No cluster needed.** The workhorse for authoring/validating. |
| `verify_cel_live_resources` | Evaluate an expression against live cluster resources. Needs kubeconfig. |
| `discover_resource_types` | List the API resource types present in the cluster. |
| `count_resources` | Count instances of a given resource type. |
| `get_resource_samples` | Pull real sample objects from the cluster (great for building accurate test data). |
| `add_rule` | Persist a rule (expression + inputs + test cases + metadata) to the library. |
| `list_rules` | Search/filter the library (by tag, category, severity, framework, text). |
| `get_rule` | Fetch one rule by id. |
| `test_rule` | Re-run a saved rule's own test cases, or run it `live`. |
| `update_rule` | Modify a saved rule. |
| `remove_rule` | Delete a saved rule by id. |

---

## Workflow A — Create & validate a rule (no cluster)

This is the default loop. Iterate here until the expression is correct, *then* optionally
save or run it live.

1. **Clarify the intent.** What resource(s)? What condition makes a resource compliant?
   Pin down the namespace/scope and the exact field path.

2. **Pick the input shape.** Each input becomes a CEL variable bound to a Kubernetes
   **List** object (`{"items":[...]}`). Decide the variable name, `group`/`version`/`resource`.
   When unsure of the real field layout, use `get_resource_samples` first.

3. **Write the expression** following the rules in
   [references/cel-cookbook.md](references/cel-cookbook.md). The critical one: data is
   List-wrapped, so iterate with `<var>.items.all(x, ...)` / `.exists(...)`.

4. **Call `verify_cel_with_tests`** with at least a happy-path case (`expected_result: true`)
   and a failure case (`expected_result: false`), plus an empty-list edge case.

   ```json
   {
     "expression": "pods.items.all(p, has(p.spec.securityContext) && p.spec.securityContext.runAsNonRoot == true)",
     "inputs": [
       {"name": "pods", "type": "kubernetes",
        "kubernetes": {"version": "v1", "resource": "pods", "namespace": "default"}}
     ],
     "test_cases": [
       {"description": "non-root pod passes", "expected_result": true,
        "inputs": {"pods": {"items": [{"metadata": {"name": "ok"}, "spec": {"securityContext": {"runAsNonRoot": true}}}]}}},
       {"description": "root pod fails", "expected_result": false,
        "inputs": {"pods": {"items": [{"metadata": {"name": "bad"}, "spec": {"securityContext": {"runAsNonRoot": false}}}]}}},
       {"description": "empty list", "expected_result": true,
        "inputs": {"pods": {"items": []}}}
     ]
   }
   ```

5. **Read the result.** The server returns per-test PASS/FAIL. If a case is wrong, decide
   whether the *expression* or the *expected_result* is mistaken, fix, and re-run. Repeat
   until all cases pass. Watch for the empty-list case: `all()` over `[]` is `true`,
   `exists()` over `[]` is `false` — make sure that matches your intent.

6. Report the final expression, the inputs, and the passing test matrix to the user.

## Workflow B — Run against a live cluster

Use when the user wants to know whether the *actual* cluster is compliant.

1. Confirm kubeconfig is wired up (see Prerequisites). If not, say so — don't silently
   return mock results.
2. Optionally `discover_resource_types` / `count_resources` to confirm the resource exists
   and is populated.
3. Call `verify_cel_live_resources`. **Expression must be List-form** (`<var>.items.all(...)`),
   because each input is fetched as a List.

   ```json
   {
     "expression": "deployments.items.all(d, d.spec.replicas >= 2)",
     "inputs": [
       {"name": "deployments", "group": "apps", "version": "v1",
        "resource": "deployments", "namespace": "production"}
     ]
   }
   ```
4. Report which resources passed/failed. If the result is surprising, pull samples with
   `get_resource_samples` and re-validate the expression in Workflow A against that real
   data before blaming the cluster.

## Workflow C — Manage the rule library

- **Save** a validated rule with `add_rule` (include `name`, `description`, `expression`,
  `inputs`, and ideally `test_cases`, `category`, `severity`, `tags`, and `metadata`
  with `compliance_framework`/`control_ids`). The full schema is in
  [references/examples.md](references/examples.md). Only save expressions that have
  already passed Workflow A.
- **Find** rules with `list_rules` (filter by `category`, `severity`, `tags`,
  `compliance_framework`, `resource_type`, or free-text `search_text`).
- **Inspect** one with `get_rule` (needs the rule id from `list_rules`).
- **Re-test** a saved rule with `test_rule` — `test_mode: "test_cases"` (default) replays
  its stored cases; `test_mode: "live"` runs it against the cluster.
- **Edit** with `update_rule` (pass `rule_id` + only the fields to change). After changing
  an expression, re-run its test cases.
- **Delete** with `remove_rule` (needs `rule_id`).

---

## Guardrails

- Never claim a CEL expression "works" without a passing `verify_cel_with_tests` /
  `verify_cel_live_resources` result to back it up.
- `verify_cel_with_tests` input items use `{"name","type","kubernetes":{...}}`;
  `verify_cel_live_resources` items are flat `{"name","group","version","resource","namespace"}`.
  Don't mix the two shapes.
- Test data must be a List: `{"items":[...]}`. A bare object (no `items`) will not iterate.
- Prefer `has(x.field)` guards before dereferencing optional fields — missing keys raise
  errors that surface as a failed/no-such-key evaluation.
- Before `add_rule`, check `list_rules` for a near-duplicate and update it instead of
  creating a second copy.

See [references/cel-cookbook.md](references/cel-cookbook.md) for CEL syntax patterns and
[references/examples.md](references/examples.md) for full tool-call payloads.
