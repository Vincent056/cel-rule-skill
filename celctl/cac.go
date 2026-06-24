// cac.go — native support for ComplianceAsCode/content CEL rules.
//
// A cac-content CEL rule lives in <rule-dir>/cel/shared.yml with this shape:
//
//	check_type: Platform
//	failure_reason: |- ...
//	inputs:
//	  - name: hcoList
//	    kubernetes_input_spec:
//	      api_version: hco.kubevirt.io/v1beta1   # group/version (or just version for core)
//	      resource: hyperconvergeds
//	      resource_name: kubevirt-hyperconverged # optional
//	      resource_namespace: openshift-cnv      # optional
//	expression: |
//	  hcoList.items.all(h, ...)
//
// Binding semantics MUST match the Compliance Operator scanner
// (compliance-sdk pkg/fetchers + pkg/scanner toCelValue):
//   - resource_name SET   -> variable bound to the single object  (use h.spec...)
//   - resource_name UNSET -> variable bound to {apiVersion,kind,items:[...]} (use h.items...)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type cacInputSpec struct {
	APIVersion        string `yaml:"api_version"`
	Resource          string `yaml:"resource"`
	ResourceName      string `yaml:"resource_name"`
	ResourceNamespace string `yaml:"resource_namespace"`
}

type cacInput struct {
	Name string       `yaml:"name"`
	Spec cacInputSpec `yaml:"kubernetes_input_spec"`
}

type cacRule struct {
	CheckType     string     `yaml:"check_type"`
	FailureReason string     `yaml:"failure_reason"`
	Inputs        []cacInput `yaml:"inputs"`
	Expression    string     `yaml:"expression"`

	dir       string // rule directory (for naming/fixtures)
	sharedYml string // path to shared.yml
}

// cacCase is one unit-test case in a fixtures file (cel/tests/*.yaml).
type cacCase struct {
	Name   string                 `yaml:"name"`
	Expect bool                   `yaml:"expect"`
	Inputs map[string]interface{} `yaml:"inputs"`
}

func cmdCac(args []string) int {
	if len(args) == 0 {
		return fail("cac subcommand required: lint|test|live")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "lint":
		return cacLint(rest)
	case "test":
		return cacTest(rest)
	case "live":
		return cacLive(rest)
	default:
		return fail("unknown cac subcommand: %s", sub)
	}
}

// resolveSharedYml accepts a rule directory, a cel/ directory, or a shared.yml
// path and returns the shared.yml path.
func resolveSharedYml(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	for _, cand := range []string{
		filepath.Join(path, "cel", "shared.yml"),
		filepath.Join(path, "shared.yml"),
	} {
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no cel/shared.yml found under %s", path)
}

func loadCacRule(path string) (*cacRule, error) {
	shared, err := resolveSharedYml(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(shared)
	if err != nil {
		return nil, err
	}
	var r cacRule
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", shared, err)
	}
	if r.Expression == "" {
		return nil, fmt.Errorf("%s has no expression", shared)
	}
	r.sharedYml = shared
	r.dir = filepath.Dir(filepath.Dir(shared)) // .../<rule>/cel/shared.yml -> <rule>
	return &r, nil
}

// emptyBinding returns the zero-value binding the scanner would produce for an
// input when the cluster has nothing: an empty object for single-object inputs,
// an empty list wrapper for list inputs.
func (in cacInput) emptyBinding() interface{} {
	if in.Spec.ResourceName != "" {
		// Minimal object skeleton so a smoke-eval doesn't error on the common
		// top-level paths (.metadata/.spec/.status/.data). Deeper fields stay
		// absent, so has()-guards short-circuit cleanly. Not a substitute for
		// real fixtures — see `cac test`.
		return map[string]interface{}{
			"metadata": map[string]interface{}{"name": "", "namespace": ""},
			"spec":     map[string]interface{}{},
			"status":   map[string]interface{}{},
			"data":     map[string]interface{}{},
		}
	}
	return map[string]interface{}{"apiVersion": "v1", "kind": "List", "items": []interface{}{}}
}

// normalizeBinding coerces user/cluster-supplied data into the exact shape the
// scanner binds for this input.
func (in cacInput) normalizeBinding(raw interface{}) interface{} {
	if in.Spec.ResourceName != "" {
		// Single object. If a list/wrapper was given, take the first item.
		if m, ok := raw.(map[string]interface{}); ok {
			if items, ok := m["items"].([]interface{}); ok {
				if len(items) > 0 {
					return items[0]
				}
				return map[string]interface{}{}
			}
			return m
		}
		return raw
	}
	// List input -> {items: [...]}.
	switch v := raw.(type) {
	case []interface{}:
		return map[string]interface{}{"items": v}
	case map[string]interface{}:
		if _, ok := v["items"]; ok {
			return v // already a List wrapper
		}
		return map[string]interface{}{"items": []interface{}{v}} // single object -> wrap
	default:
		return map[string]interface{}{"items": []interface{}{}}
	}
}

// cacLint: parse + compile + evaluate against empty bindings. This catches the
// most common authoring bug — iterating a list input without `.items` (the
// scanner binds it as a map, so `x.all(...)` fails at runtime).
func cacLint(args []string) int {
	fs := newFlags("cac lint")
	fs.Parse(reorderArgs(args))
	path := fs.Arg(0)
	if path == "" {
		return fail("usage: celctl cac lint <rule-dir|shared.yml>")
	}
	rule, err := loadCacRule(path)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("rule: %s\n", rule.dir)
	for _, in := range rule.Inputs {
		kind := "list (use ." + in.Name + ".items...)"
		if in.Spec.ResourceName != "" {
			kind = "single object (use " + in.Name + ".spec...)"
		}
		fmt.Printf("  input %-12s %-40s %s\n", in.Name, in.Spec.APIVersion+"/"+in.Spec.Resource, kind)
	}

	vars := map[string]interface{}{}
	for _, in := range rule.Inputs {
		vars[in.Name] = in.emptyBinding()
	}
	_, err = evalExpr(rule.Expression, vars)
	if err != nil {
		fmt.Printf("\n❌ LINT FAILED: %v\n", err)
		if strings.Contains(err.Error(), "no such key") || strings.Contains(err.Error(), "no matching overload") {
			fmt.Println("   Hint: a list input (no resource_name) is bound as {items:[...]}.")
			fmt.Println("   Iterate it with <name>.items.all(...) / .exists(...) / .filter(...), not <name>.all(...).")
		}
		return 1
	}
	fmt.Printf("\n✅ compiles & evaluates (empty-cluster result is vacuous). Add fixtures with `cac test` for real coverage.\n")
	return 0
}

// cacTest: evaluate against fixtures. Either inline --mock name=file (one shot,
// prints the boolean) or --cases file.yaml (multiple expected cases).
func cacTest(args []string) int {
	fs := newFlags("cac test")
	casesPath := fs.String("cases", "", "fixtures YAML/JSON: list of {name, expect, inputs}")
	expect := fs.String("expect", "", "for --mock mode: assert result is true|false")
	var mocks multiFlag
	fs.Var(&mocks, "mock", "bind input data: name=path.json|yaml (repeatable)")
	fs.Parse(reorderArgs(args))
	path := fs.Arg(0)
	if path == "" {
		return fail("usage: celctl cac test <rule-dir|shared.yml> (--cases f | --mock name=f ...)")
	}
	rule, err := loadCacRule(path)
	if err != nil {
		return fail("%v", err)
	}

	// Auto-discover cel/tests/*.{yaml,yml,json} if no fixtures given.
	if *casesPath == "" && len(mocks) == 0 {
		if d := filepath.Join(rule.dir, "cel", "tests"); dirExists(d) {
			fmt.Printf("(using fixtures in %s)\n", d)
			return cacRunCaseDir(rule, d)
		}
		return fail("no fixtures: pass --cases <file>, --mock name=file, or add cel/tests/*.yaml")
	}

	if *casesPath != "" {
		cases, err := loadCases(*casesPath)
		if err != nil {
			return fail("%v", err)
		}
		return cacRunCases(rule, cases)
	}

	// --mock one-shot mode.
	vars, err := bindMocks(rule, mocks)
	if err != nil {
		return fail("%v", err)
	}
	got, err := evalExpr(rule.Expression, vars)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%v\n", got)
	if *expect != "" {
		want := *expect == "true"
		if got != want {
			fmt.Printf("❌ expected %v, got %v\n", want, got)
			return 1
		}
		fmt.Printf("✅ matches expected %v\n", want)
	}
	if !got {
		return 1
	}
	return 0
}

func bindMocks(rule *cacRule, mocks multiFlag) (map[string]interface{}, error) {
	byName := map[string]cacInput{}
	for _, in := range rule.Inputs {
		byName[in.Name] = in
	}
	vars := map[string]interface{}{}
	for _, in := range rule.Inputs {
		vars[in.Name] = in.emptyBinding() // default missing inputs to empty
	}
	for _, m := range mocks {
		name, path, ok := strings.Cut(m, "=")
		if !ok {
			return nil, fmt.Errorf("bad --mock %q (want name=path)", m)
		}
		in, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("--mock %q: no input named %q in rule", name, name)
		}
		raw, err := loadStructured(path)
		if err != nil {
			return nil, err
		}
		vars[name] = in.normalizeBinding(raw)
	}
	return vars, nil
}

func cacRunCaseDir(rule *cacRule, dir string) int {
	entries, _ := os.ReadDir(dir)
	pass, total := 0, 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !hasAnySuffix(e.Name(), ".yaml", ".yml", ".json") {
			continue
		}
		cases, err := loadCases(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Printf("  ❌ %s: %v\n", e.Name(), err)
			continue
		}
		for _, c := range cases {
			total++
			if runOneCase(rule, c) {
				pass++
			}
		}
	}
	fmt.Printf("\n%d/%d cases passed\n", pass, total)
	if pass != total {
		return 1
	}
	return 0
}

func cacRunCases(rule *cacRule, cases []cacCase) int {
	pass := 0
	for _, c := range cases {
		if runOneCase(rule, c) {
			pass++
		}
	}
	fmt.Printf("\n%d/%d cases passed\n", pass, len(cases))
	if pass != len(cases) {
		return 1
	}
	return 0
}

func runOneCase(rule *cacRule, c cacCase) bool {
	byName := map[string]cacInput{}
	for _, in := range rule.Inputs {
		byName[in.Name] = in
	}
	vars := map[string]interface{}{}
	for _, in := range rule.Inputs {
		vars[in.Name] = in.emptyBinding()
	}
	for name, raw := range c.Inputs {
		if in, ok := byName[name]; ok {
			vars[name] = in.normalizeBinding(raw)
		} else {
			vars[name] = raw
		}
	}
	name := c.Name
	if name == "" {
		name = "case"
	}
	got, err := evalExpr(rule.Expression, vars)
	if err != nil {
		fmt.Printf("  ❌ %s — %v\n", name, err)
		return false
	}
	if got == c.Expect {
		fmt.Printf("  ✅ %s — %v\n", name, got)
		return true
	}
	fmt.Printf("  ❌ %s — got %v, expected %v\n", name, got, c.Expect)
	return false
}

// cacLive: fetch inputs from the cluster via kubectl, then evaluate.
func cacLive(args []string) int {
	fs := newFlags("cac live")
	fs.Parse(reorderArgs(args))
	path := fs.Arg(0)
	if path == "" {
		return fail("usage: celctl cac live <rule-dir|shared.yml>")
	}
	rule, err := loadCacRule(path)
	if err != nil {
		return fail("%v", err)
	}
	vars := map[string]interface{}{}
	for _, in := range rule.Inputs {
		data, err := cacKubectlFetch(in.Spec)
		if err != nil {
			return fail("fetch %q: %v", in.Name, err)
		}
		vars[in.Name] = in.normalizeBinding(data)
		fmt.Printf("  fetched %s (%s)\n", in.Name, describeBinding(vars[in.Name]))
	}
	got, err := evalExpr(rule.Expression, vars)
	if err != nil {
		return fail("%v", err)
	}
	if got {
		fmt.Printf("\n✅ PASS (compliant)\n")
		return 0
	}
	fmt.Printf("\n❌ FAIL (non-compliant)\n")
	if rule.FailureReason != "" {
		fmt.Printf("   %s\n", strings.TrimSpace(rule.FailureReason))
	}
	return 1
}

func cacKubectlFetch(spec cacInputSpec) (interface{}, error) {
	group, version := splitAPIVersion(spec.APIVersion)
	res := spec.Resource
	if version != "" {
		res += "." + version
	}
	if group != "" {
		res += "." + group
	}
	kargs := []string{"get", res, "-o", "json"}
	if spec.ResourceName != "" {
		kargs = []string{"get", res, spec.ResourceName, "-o", "json"}
	}
	if spec.ResourceNamespace != "" {
		kargs = append(kargs, "-n", spec.ResourceNamespace)
	} else if spec.ResourceName == "" {
		kargs = append(kargs, "-A")
	}
	out, err := runKubectl(kargs...)
	if err != nil {
		return nil, err
	}
	var v interface{}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// ----- helpers -----

func splitAPIVersion(av string) (group, version string) {
	if i := strings.LastIndex(av, "/"); i >= 0 {
		return av[:i], av[i+1:]
	}
	return "", av // core group
}

func loadStructured(path string) (interface{}, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v interface{}
	if hasAnySuffix(path, ".yaml", ".yml") {
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else {
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return normalizeYAML(v), nil
}

func loadCases(path string) ([]cacCase, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []cacCase
	if hasAnySuffix(path, ".json") {
		err = json.Unmarshal(b, &cases)
	} else {
		err = yaml.Unmarshal(b, &cases)
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range cases {
		cases[i].Inputs = normalizeYAMLMap(cases[i].Inputs)
	}
	return cases, nil
}

// normalizeYAML converts map[interface{}]interface{} (yaml.v3 can emit these
// for nested maps) into map[string]interface{} so cel-go can consume it.
func normalizeYAML(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		for k, val := range x {
			x[k] = normalizeYAML(val)
		}
		return x
	case map[interface{}]interface{}:
		m := map[string]interface{}{}
		for k, val := range x {
			m[fmt.Sprintf("%v", k)] = normalizeYAML(val)
		}
		return m
	case []interface{}:
		for i, val := range x {
			x[i] = normalizeYAML(val)
		}
		return x
	default:
		return v
	}
}

func normalizeYAMLMap(m map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range m {
		out[k] = normalizeYAML(v)
	}
	return out
}

func describeBinding(v interface{}) string {
	if m, ok := v.(map[string]interface{}); ok {
		if items, ok := m["items"].([]interface{}); ok {
			return fmt.Sprintf("%d item(s)", len(items))
		}
		return "single object"
	}
	return "value"
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}
