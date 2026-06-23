# Tool-call payload reference

Exact argument shapes for each MCP tool, copy-paste ready. Field names matter — the two
verify tools use *different* input shapes.

## verify_cel_with_tests  (no cluster)

Inputs are nested under a `type` + `kubernetes`/`file`/`http` block. Test data is inline.

```json
{
  "expression": "namespaces.items.all(ns, networkpolicies.items.exists(np, np.metadata.namespace == ns.metadata.name))",
  "inputs": [
    {"name": "namespaces", "type": "kubernetes",
     "kubernetes": {"version": "v1", "resource": "namespaces"}},
    {"name": "networkpolicies", "type": "kubernetes",
     "kubernetes": {"group": "networking.k8s.io", "version": "v1", "resource": "networkpolicies"}}
  ],
  "test_cases": [
    {"description": "ns with matching netpol passes", "expected_result": true,
     "inputs": {
       "namespaces": {"items": [{"metadata": {"name": "app"}}]},
       "networkpolicies": {"items": [{"metadata": {"name": "default-deny", "namespace": "app"}}]}
     }},
    {"description": "ns without netpol fails", "expected_result": false,
     "inputs": {
       "namespaces": {"items": [{"metadata": {"name": "app"}}]},
       "networkpolicies": {"items": []}
     }}
  ]
}
```

A `file` input instead of `kubernetes`:

```json
{"name": "cfg", "type": "file", "file": {"path": "/etc/app/config.yaml", "format": "yaml"}}
```

## verify_cel_live_resources  (needs kubeconfig)

Input fields are **flat** — no `type`, no nested `kubernetes` block.

```json
{
  "expression": "deployments.items.all(d, d.spec.replicas >= 2)",
  "inputs": [
    {"name": "deployments", "group": "apps", "version": "v1",
     "resource": "deployments", "namespace": "production"}
  ]
}
```

## Discovery tools

```json
// discover_resource_types — usually no args, or scope by group
{}

// count_resources
{"resource_type": "pods", "namespace": "default"}

// get_resource_samples — pull real objects to model test data on
{"resource_type": "configmaps", "namespace": "openshift-etcd", "max_samples": 3}
```

## add_rule  (save a validated rule)

```json
{
  "name": "All Namespaces Network Policy Requirement",
  "description": "Every namespace must have at least one NetworkPolicy.",
  "expression": "namespaces.items.all(ns, networkpolicies.items.exists(np, np.metadata.namespace == ns.metadata.name))",
  "inputs": [
    {"name": "namespaces", "type": "kubernetes",
     "kubernetes": {"version": "v1", "resource": "namespaces"}},
    {"name": "networkpolicies", "type": "kubernetes",
     "kubernetes": {"group": "networking.k8s.io", "version": "v1", "resource": "networkpolicies"}}
  ],
  "category": "security",
  "severity": "high",
  "tags": ["network-security", "network-policy"],
  "test_cases": [
    {"description": "ns with netpol", "expected_result": true,
     "inputs": {"namespaces": {"items": [{"metadata": {"name": "app"}}]},
                "networkpolicies": {"items": [{"metadata": {"namespace": "app"}}]}}}
  ],
  "metadata": {
    "compliance_framework": "CIS",
    "control_ids": ["5.3.2"],
    "references": ["https://www.cisecurity.org/benchmark/kubernetes"]
  }
}
```

## list_rules / get_rule / test_rule / update_rule / remove_rule

```json
// list_rules — all args optional
{"category": "security", "severity": "high", "search_text": "network", "verified_only": true, "page_size": 20}

// get_rule
{"rule_id": "93ac685f-ff17-4b4d-b0bd-658732c92ff2"}

// test_rule — replay stored cases, or run live
{"rule_id": "93ac685f-...", "test_mode": "test_cases"}
{"rule_id": "93ac685f-...", "test_mode": "live"}

// update_rule — rule_id + only the fields to change
{"rule_id": "93ac685f-...", "severity": "critical", "expression": "<new expr>"}

// remove_rule
{"rule_id": "93ac685f-..."}
```

## Saved-rule storage format (FileRuleStore, `./rules-library/<uuid>.json`)

Note: stored `test_cases[].test_data` values are **JSON strings**, and the data is a
List object. `add_rule`/`test_rule` accept the friendlier nested `inputs` form above and
the server converts it.

```json
{
  "id": "2ac55628-8557-4f40-81f2-1a82e8534686",
  "name": "etcd_client_certificate_configured",
  "expression": "etcd_configs.items.exists(config, config.data[\"pod.yaml\"].contains(\"--cert-file=\"))",
  "inputs": [{"name": "etcd_configs", "kubernetes": {"version": "v1", "resource": "configmaps", "namespace": "openshift-etcd"}}],
  "test_cases": [
    {"description": "correct path", "expected_result": true,
     "test_data": {"etcd_configs": "{\"items\":[{\"data\":{\"pod.yaml\":\"etcd --cert-file=...\"}}]}"}}
  ],
  "category": "security", "severity": "medium",
  "metadata": {"compliance_framework": "CIS"}
}
```
