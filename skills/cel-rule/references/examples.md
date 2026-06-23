# celctl reference — rule file format & commands

`celctl` replaces the cel-rpc-server MCP server. The rule JSON format is **compatible**
with cel-rpc-server's `rules-library/*.json`, so existing files work unchanged.

## Rule file format

```json
{
  "id": "all-namespaces-network-policy",
  "name": "All Namespaces Network Policy Requirement",
  "description": "Every namespace must have at least one NetworkPolicy.",
  "expression": "namespaces.items.all(ns, networkpolicies.items.exists(np, np.metadata.namespace == ns.metadata.name))",
  "inputs": [
    {"name": "namespaces",      "kubernetes": {"version": "v1", "resource": "namespaces"}},
    {"name": "networkpolicies", "kubernetes": {"group": "networking.k8s.io", "version": "v1", "resource": "networkpolicies"}}
  ],
  "category": "security",
  "severity": "high",
  "tags": ["network-security", "network-policy"],
  "test_cases": [
    {"description": "ns with matching netpol passes", "expected_result": true,
     "test_data": {
       "namespaces":      {"items": [{"metadata": {"name": "app"}}]},
       "networkpolicies": {"items": [{"metadata": {"name": "deny", "namespace": "app"}}]}
     }},
    {"description": "ns without netpol fails", "expected_result": false,
     "test_data": {
       "namespaces":      {"items": [{"metadata": {"name": "app"}}]},
       "networkpolicies": {"items": []}
     }},
    {"description": "no namespaces — vacuously true", "expected_result": true,
     "test_data": {"namespaces": {"items": []}}}
  ],
  "metadata": {
    "compliance_framework": "CIS",
    "control_ids": ["5.3.2"],
    "references": ["https://www.cisecurity.org/benchmark/kubernetes"]
  }
}
```

Notes:
- `test_data` values may be an **inline JSON object** (above) *or* a **JSON string**
  (cel-rpc-server's on-disk format, e.g. `"{\"items\":[...]}"`). celctl accepts both.
- An input not present in a test case's `test_data` defaults to an empty List `{"items":[]}`.
- `id` is optional for `rule add` (a slug is derived from `name` if omitted).

## verify — evaluate against test cases (no cluster)

```bash
# whole rule file:
celctl verify --rule myrule.json

# ad-hoc expression + a JSON array of test cases:
celctl verify --expr 'pods.items.all(p, p.metadata.name != "")' --test cases.json
```

`cases.json` is just the `test_cases` array:

```json
[
  {"description": "named ok", "expected_result": true,
   "test_data": {"pods": {"items": [{"metadata": {"name": "x"}}]}}},
  {"description": "empty name fails", "expected_result": false,
   "test_data": {"pods": {"items": [{"metadata": {"name": ""}}]}}}
]
```

Output: per-case ✅/❌ and `N/M passed`. Exit code 0 only if all pass.

## eval — evaluate once, print the boolean

```bash
echo '{"items":[{"spec":{"hostNetwork":true}}]}' > pods.json
celctl eval --expr 'pods.items.filter(p, p.spec.hostNetwork == true).size() == 0' --data pods=pods.json
# prints: false   (exit 1)
```

`--data name=path.json` is repeatable; each file is the List for that variable.

## live — evaluate against the cluster (needs kubectl)

```bash
# from a rule file (uses its inputs' group/version/resource/namespace):
celctl live --rule myrule.json

# ad-hoc:
celctl live --expr 'deployments.items.all(d, d.spec.replicas >= 2)' \
            --input deployments=apps/v1/deployments:production
```

Input spec: `name=[group/]version/resource[:namespace]`
- core type, all namespaces: `pods=v1/pods`
- core type, one namespace:  `pods=v1/pods:default`
- grouped type:              `deployments=apps/v1/deployments:production`

celctl runs `kubectl get <resource>.<version>.<group> -o json` (with `-n ns` or `-A`),
binds the result, evaluates, and prints fetched counts + ✅ PASS / ❌ FAIL.

## discovery

```bash
celctl discover                              # kubectl api-resources --verbs=list -o wide
celctl samples configmaps -n openshift-etcd --max 3   # real objects to model test data on
```

## rule library

```bash
celctl rule list                             # default dir ./rules-library
celctl rule list --dir /path/to/lib --category security --tag etcd --search certificate
celctl rule get <id-or-name>
celctl rule add --file myrule.json           # validates test cases, refuses to save on failure
celctl rule test <id> --mode test_cases      # replay stored cases (default)
celctl rule test <id> --mode live            # run saved rule against the cluster
celctl rule remove <id>
```

`--dir` may appear before or after the positional id. The default library dir is
`./rules-library`.
