// celctl — a self-contained CEL rule utility for Kubernetes compliance checks.
//
// Replaces the cel-rpc-server MCP server for the create/validate/run/manage loop:
//   - evaluates CEL locally with cel-go (the same engine the server used)
//   - runs rules against a live cluster via kubectl
//   - manages a file-based rule library (compatible with cel-rpc-server's format)
//
// No server, no container, no AI keys.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"gopkg.in/yaml.v3"
)

// celEnvOptions returns the base CEL environment options used everywhere, kept
// in lock-step with the Compliance Operator scanner: stdlib + parseJSON +
// parseYAML. Variable declarations are appended by callers.
func celEnvOptions() []cel.EnvOption {
	mapStrDyn := cel.MapType(cel.StringType, cel.DynType)
	return []cel.EnvOption{
		cel.Function("parseJSON", cel.Overload("parseJSON_string",
			[]*cel.Type{cel.StringType}, mapStrDyn, cel.UnaryBinding(parseJSONString))),
		cel.Function("parseYAML", cel.Overload("parseYAML_string",
			[]*cel.Type{cel.StringType}, mapStrDyn, cel.UnaryBinding(parseYAMLString))),
	}
}

func parseJSONString(val ref.Val) ref.Val {
	s, ok := val.Value().(string)
	if !ok {
		return types.NewErr("parseJSON: argument is not a string")
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		return types.NewErr("parseJSON: %v", err)
	}
	r, err := types.NewRegistry()
	if err != nil {
		return types.NewErr("parseJSON: %v", err)
	}
	return types.NewDynamicMap(r, decoded)
}

func parseYAMLString(val ref.Val) ref.Val {
	s, ok := val.Value().(string)
	if !ok {
		return types.NewErr("parseYAML: argument is not a string")
	}
	decoded := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(s), &decoded); err != nil {
		return types.NewErr("parseYAML: %v", err)
	}
	r, err := types.NewRegistry()
	if err != nil {
		return types.NewErr("parseYAML: %v", err)
	}
	return types.NewDynamicMap(r, decoded)
}

// newFlags returns a FlagSet that prints a contextual usage line on error.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs
}

// reorderArgs moves positional args to the end so flags may appear after them.
// Go's flag package stops at the first non-flag token; this lets users write
// `rule test <id> --dir x` as well as `rule test --dir x <id>`. Assumes flags
// take values in `--flag value` or `--flag=value` form (no standalone bools).
func reorderArgs(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// ----- rule model (compatible with cel-rpc-server FileRuleStore JSON) -----

type KubernetesInput struct {
	Group        string `json:"group,omitempty"`
	Version      string `json:"version"`
	Resource     string `json:"resource"`
	Namespace    string `json:"namespace,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`
}

type RuleInput struct {
	Name       string           `json:"name"`
	Kubernetes *KubernetesInput `json:"kubernetes,omitempty"`
}

type TestCase struct {
	Description    string `json:"description,omitempty"`
	ExpectedResult bool   `json:"expected_result"`
	// TestData maps a variable name to its data. Accepts either a JSON string
	// (cel-rpc-server format) or an inline JSON object — see UnmarshalJSON.
	TestData map[string]json.RawMessage `json:"test_data"`
}

type Rule struct {
	ID          string                 `json:"id,omitempty"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Expression  string                 `json:"expression"`
	Inputs      []RuleInput            `json:"inputs"`
	TestCases   []TestCase             `json:"test_cases,omitempty"`
	Tags        []string               `json:"tags,omitempty"`
	Category    string                 `json:"category,omitempty"`
	Severity    string                 `json:"severity,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ----- CEL evaluation core -----

// evalExpr compiles expr with the given variables declared as dynamic, then
// evaluates it. The boolean result is returned. vars values must already be
// decoded JSON (map[string]interface{}, []interface{}, float64, string, ...).
//
// The environment mirrors the Compliance Operator's CEL scanner
// (compliance-sdk pkg/scanner): standard library plus the custom parseJSON /
// parseYAML functions, with every input declared as a dynamic variable. No
// extra string extensions are added, so an expression that compiles/evaluates
// here behaves the same way it will in the operator.
func evalExpr(expr string, vars map[string]interface{}) (bool, error) {
	opts := celEnvOptions()
	for name := range vars {
		opts = append(opts, cel.Variable(name, cel.DynType))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return false, fmt.Errorf("env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return false, fmt.Errorf("compile: %w", iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("program: %w", err)
	}
	out, _, err := prg.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("eval: %w", err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("expression did not return a boolean (got %T: %v)", out.Value(), out.Value())
	}
	return b, nil
}

// decodeTestData turns a test case's TestData (each value a JSON string or
// inline object) into decoded Go values ready for CEL. Inputs not present
// default to an empty List, matching the server's behaviour.
func decodeTestData(td map[string]json.RawMessage, inputs []RuleInput) (map[string]interface{}, error) {
	vars := map[string]interface{}{}
	for name, raw := range td {
		v, err := decodeMaybeString(raw)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", name, err)
		}
		vars[name] = v
	}
	// Default missing declared inputs to an empty list wrapper.
	for _, in := range inputs {
		if _, ok := vars[in.Name]; !ok {
			vars[in.Name] = map[string]interface{}{"items": []interface{}{}}
		}
	}
	return vars, nil
}

// decodeMaybeString accepts raw that is either a JSON string containing JSON
// (e.g. "{\"items\":[]}") or an inline JSON value, and returns the decoded value.
func decodeMaybeString(raw json.RawMessage) (interface{}, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		var v interface{}
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, fmt.Errorf("value is a string but not valid JSON: %w", err)
		}
		return v, nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// ----- commands -----

func cmdVerify(args []string) int {
	fs := newFlags("verify")
	rulePath := fs.String("rule", "", "path to a rule JSON file (with test_cases)")
	expr := fs.String("expr", "", "CEL expression (alternative to --rule)")
	dataPath := fs.String("test", "", "path to a JSON file of test cases (used with --expr)")
	fs.Parse(reorderArgs(args))

	var rule Rule
	if *rulePath != "" {
		if err := loadJSON(*rulePath, &rule); err != nil {
			return fail("load rule: %v", err)
		}
	} else if *expr != "" {
		rule.Expression = *expr
		if *dataPath != "" {
			if err := loadJSON(*dataPath, &rule.TestCases); err != nil {
				return fail("load test cases: %v", err)
			}
		}
	} else {
		return fail("provide --rule <file> or --expr <cel> [--test <file>]")
	}

	if len(rule.TestCases) == 0 {
		return fail("no test cases to run")
	}

	pass, total := 0, len(rule.TestCases)
	for i, tc := range rule.TestCases {
		desc := tc.Description
		if desc == "" {
			desc = fmt.Sprintf("test case %d", i+1)
		}
		vars, err := decodeTestData(tc.TestData, rule.Inputs)
		if err != nil {
			fmt.Printf("  ❌ %s — bad test data: %v\n", desc, err)
			continue
		}
		got, err := evalExpr(rule.Expression, vars)
		if err != nil {
			fmt.Printf("  ❌ %s — %v\n", desc, err)
			continue
		}
		if got == tc.ExpectedResult {
			pass++
			fmt.Printf("  ✅ %s — got %v (expected %v)\n", desc, got, tc.ExpectedResult)
		} else {
			fmt.Printf("  ❌ %s — got %v, expected %v\n", desc, got, tc.ExpectedResult)
		}
	}
	fmt.Printf("\n%d/%d passed\n", pass, total)
	if pass != total {
		return 1
	}
	return 0
}

func cmdEval(args []string) int {
	fs := newFlags("eval")
	expr := fs.String("expr", "", "CEL expression (required)")
	var datas multiFlag
	fs.Var(&datas, "data", "variable binding name=path.json or name=@- for stdin (repeatable)")
	fs.Parse(reorderArgs(args))
	if *expr == "" {
		return fail("--expr is required")
	}
	vars := map[string]interface{}{}
	for _, d := range datas {
		name, path, ok := strings.Cut(d, "=")
		if !ok {
			return fail("bad --data %q (want name=path.json)", d)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fail("read %s: %v", path, err)
		}
		v, err := decodeMaybeString(raw)
		if err != nil {
			return fail("decode %s: %v", path, err)
		}
		vars[name] = v
	}
	got, err := evalExpr(*expr, vars)
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%v\n", got)
	if !got {
		return 1
	}
	return 0
}

func cmdLive(args []string) int {
	fs := newFlags("live")
	rulePath := fs.String("rule", "", "rule JSON file (uses its expression + inputs)")
	expr := fs.String("expr", "", "CEL expression (alternative to --rule)")
	var inputs multiFlag
	fs.Var(&inputs, "input", "name=[group/]version/resource[:namespace] (repeatable, used with --expr)")
	fs.Parse(reorderArgs(args))

	var rule Rule
	if *rulePath != "" {
		if err := loadJSON(*rulePath, &rule); err != nil {
			return fail("load rule: %v", err)
		}
	} else if *expr != "" {
		rule.Expression = *expr
		for _, in := range inputs {
			ri, err := parseInputSpec(in)
			if err != nil {
				return fail("%v", err)
			}
			rule.Inputs = append(rule.Inputs, ri)
		}
	} else {
		return fail("provide --rule <file> or --expr <cel> --input ...")
	}
	if len(rule.Inputs) == 0 {
		return fail("no inputs defined")
	}

	vars := map[string]interface{}{}
	for _, in := range rule.Inputs {
		if in.Kubernetes == nil {
			return fail("input %q has no kubernetes config", in.Name)
		}
		data, err := kubectlGet(in.Kubernetes)
		if err != nil {
			return fail("fetch %q: %v", in.Name, err)
		}
		vars[in.Name] = data
		count := 0
		if m, ok := data.(map[string]interface{}); ok {
			if items, ok := m["items"].([]interface{}); ok {
				count = len(items)
			}
		}
		fmt.Printf("  fetched %s: %d item(s)\n", in.Name, count)
	}
	got, err := evalExpr(rule.Expression, vars)
	if err != nil {
		return fail("%v", err)
	}
	if got {
		fmt.Printf("\n✅ PASS — cluster satisfies the expression\n")
		return 0
	}
	fmt.Printf("\n❌ FAIL — cluster does not satisfy the expression\n")
	return 1
}

// ----- rule library -----

func cmdRule(args []string) int {
	if len(args) == 0 {
		return fail("rule subcommand required: list|get|add|test|remove")
	}
	sub, rest := args[0], args[1:]
	fs := newFlags("rule " + sub)
	dir := fs.String("dir", "./rules-library", "rule library directory")
	switch sub {
	case "list":
		tag := fs.String("tag", "", "filter by tag")
		category := fs.String("category", "", "filter by category")
		search := fs.String("search", "", "free-text search over name/description/expression")
		fs.Parse(reorderArgs(rest))
		rules, err := loadLibrary(*dir)
		if err != nil {
			return fail("%v", err)
		}
		n := 0
		for _, r := range rules {
			if *category != "" && r.Category != *category {
				continue
			}
			if *tag != "" && !contains(r.Tags, *tag) {
				continue
			}
			if *search != "" && !matchesText(r, *search) {
				continue
			}
			fmt.Printf("• %s  [%s]\n    id: %s  category=%s severity=%s tags=%v\n",
				r.Name, short(r.Expression), r.ID, r.Category, r.Severity, r.Tags)
			n++
		}
		fmt.Printf("\n%d rule(s)\n", n)
		return 0
	case "get":
		fs.Parse(reorderArgs(rest))
		id := fs.Arg(0)
		if id == "" {
			return fail("usage: celctl rule get <id>")
		}
		r, _, err := findRule(*dir, id)
		if err != nil {
			return fail("%v", err)
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
		return 0
	case "add":
		file := fs.String("file", "", "rule JSON file to add (id assigned if missing)")
		fs.Parse(reorderArgs(rest))
		if *file == "" {
			return fail("usage: celctl rule add --file rule.json")
		}
		var r Rule
		if err := loadJSON(*file, &r); err != nil {
			return fail("%v", err)
		}
		if r.Expression == "" || r.Name == "" {
			return fail("rule needs at least name and expression")
		}
		if r.ID == "" {
			r.ID = newID(r.Name)
		}
		// Refuse to save unless test cases pass (when present).
		if len(r.TestCases) > 0 {
			fmt.Println("Validating test cases before saving...")
			if code := verifyRule(&r); code != 0 {
				return fail("not saved: test cases failed")
			}
		}
		if err := os.MkdirAll(*dir, 0o755); err != nil {
			return fail("%v", err)
		}
		out := filepath.Join(*dir, r.ID+".json")
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("saved %s (id=%s)\n", out, r.ID)
		return 0
	case "test":
		mode := fs.String("mode", "test_cases", "test_cases|live")
		fs.Parse(reorderArgs(rest))
		id := fs.Arg(0)
		if id == "" {
			return fail("usage: celctl rule test <id> [--mode test_cases|live]")
		}
		r, path, err := findRule(*dir, id)
		if err != nil {
			return fail("%v", err)
		}
		if *mode == "live" {
			return cmdLive([]string{"--rule", path})
		}
		return verifyRule(r)
	case "remove":
		fs.Parse(reorderArgs(rest))
		id := fs.Arg(0)
		if id == "" {
			return fail("usage: celctl rule remove <id>")
		}
		_, path, err := findRule(*dir, id)
		if err != nil {
			return fail("%v", err)
		}
		if err := os.Remove(path); err != nil {
			return fail("%v", err)
		}
		fmt.Printf("removed %s\n", path)
		return 0
	default:
		return fail("unknown rule subcommand: %s", sub)
	}
}

// ----- discovery (thin kubectl wrappers) -----

func cmdDiscover(args []string) int {
	out, err := runKubectl("api-resources", "--verbs=list", "-o", "wide")
	if err != nil {
		return fail("%v", err)
	}
	fmt.Print(out)
	return 0
}

func cmdSamples(args []string) int {
	fs := newFlags("samples")
	ns := fs.String("n", "", "namespace")
	max := fs.Int("max", 3, "max samples")
	fs.Parse(reorderArgs(args))
	res := fs.Arg(0)
	if res == "" {
		return fail("usage: celctl samples <resource> [-n ns] [--max N]")
	}
	kargs := []string{"get", res, "-o", "json"}
	if *ns != "" {
		kargs = append(kargs, "-n", *ns)
	} else {
		kargs = append(kargs, "-A")
	}
	out, err := runKubectl(kargs...)
	if err != nil {
		return fail("%v", err)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return fail("%v", err)
	}
	if len(list.Items) > *max {
		list.Items = list.Items[:*max]
	}
	b, _ := json.MarshalIndent(map[string]interface{}{"items": list.Items}, "", "  ")
	fmt.Println(string(b))
	return 0
}

// ----- helpers -----

func verifyRule(r *Rule) int {
	if len(r.TestCases) == 0 {
		fmt.Println("(no test cases)")
		return 0
	}
	pass := 0
	for i, tc := range r.TestCases {
		desc := tc.Description
		if desc == "" {
			desc = fmt.Sprintf("test case %d", i+1)
		}
		vars, err := decodeTestData(tc.TestData, r.Inputs)
		if err != nil {
			fmt.Printf("  ❌ %s — bad test data: %v\n", desc, err)
			continue
		}
		got, err := evalExpr(r.Expression, vars)
		if err != nil {
			fmt.Printf("  ❌ %s — %v\n", desc, err)
			continue
		}
		if got == tc.ExpectedResult {
			pass++
			fmt.Printf("  ✅ %s\n", desc)
		} else {
			fmt.Printf("  ❌ %s — got %v, expected %v\n", desc, got, tc.ExpectedResult)
		}
	}
	fmt.Printf("%d/%d passed\n", pass, len(r.TestCases))
	if pass != len(r.TestCases) {
		return 1
	}
	return 0
}

func kubectlGet(k *KubernetesInput) (interface{}, error) {
	res := k.Resource
	if k.Version != "" {
		res += "." + k.Version
	}
	if k.Group != "" {
		res += "." + k.Group
	}
	kargs := []string{"get", res, "-o", "json"}
	if k.ResourceName != "" {
		kargs = []string{"get", res, k.ResourceName, "-o", "json"}
	}
	if k.Namespace != "" {
		kargs = append(kargs, "-n", k.Namespace)
	} else if k.ResourceName == "" {
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
	// A single named object isn't a List — wrap it so .items works uniformly.
	if m, ok := v.(map[string]interface{}); ok {
		if _, hasItems := m["items"]; !hasItems {
			return map[string]interface{}{"items": []interface{}{m}}, nil
		}
	}
	return v, nil
}

func runKubectl(args ...string) (string, error) {
	cmd := exec.Command("kubectl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func parseInputSpec(s string) (RuleInput, error) {
	name, rest, ok := strings.Cut(s, "=")
	if !ok {
		return RuleInput{}, fmt.Errorf("bad --input %q (want name=[group/]version/resource[:namespace])", s)
	}
	gvr, ns, _ := strings.Cut(rest, ":")
	parts := strings.Split(gvr, "/")
	k := &KubernetesInput{Namespace: ns}
	switch len(parts) {
	case 2: // version/resource
		k.Version, k.Resource = parts[0], parts[1]
	case 3: // group/version/resource
		k.Group, k.Version, k.Resource = parts[0], parts[1], parts[2]
	default:
		return RuleInput{}, fmt.Errorf("bad gvr %q in --input (want [group/]version/resource)", gvr)
	}
	return RuleInput{Name: name, Kubernetes: k}, nil
}

func loadLibrary(dir string) ([]*Rule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read library %s: %w", dir, err)
	}
	var rules []*Rule
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var r Rule
		if err := loadJSON(filepath.Join(dir, e.Name()), &r); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skip %s: %v\n", e.Name(), err)
			continue
		}
		rules = append(rules, &r)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	return rules, nil
}

func findRule(dir, id string) (*Rule, string, error) {
	path := filepath.Join(dir, id+".json")
	if _, err := os.Stat(path); err == nil {
		var r Rule
		if err := loadJSON(path, &r); err != nil {
			return nil, "", err
		}
		return &r, path, nil
	}
	// Fall back to matching by id or name field inside files.
	rules, err := loadLibrary(dir)
	if err != nil {
		return nil, "", err
	}
	for _, r := range rules {
		if r.ID == id || r.Name == id {
			return r, filepath.Join(dir, r.ID+".json"), nil
		}
	}
	return nil, "", fmt.Errorf("rule %q not found in %s", id, dir)
}

func loadJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func matchesText(r *Rule, q string) bool {
	q = strings.ToLower(q)
	return strings.Contains(strings.ToLower(r.Name), q) ||
		strings.Contains(strings.ToLower(r.Description), q) ||
		strings.Contains(strings.ToLower(r.Expression), q)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

func newID(name string) string {
	slug := strings.ToLower(strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, name))
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "rule"
	}
	return slug
}

// flag helpers

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func fail(format string, a ...interface{}) int {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	return 2
}

func usage() {
	fmt.Print(`celctl — self-contained CEL rule utility for Kubernetes compliance

Usage:
  celctl verify   --rule rule.json                 Run a rule's test cases
  celctl verify   --expr '<cel>' --test cases.json  Run ad-hoc test cases
  celctl eval     --expr '<cel>' --data v=v.json    Evaluate once, print bool
  celctl live     --rule rule.json                 Run against live cluster (kubectl)
  celctl live     --expr '<cel>' --input pods=v1/pods:default
  celctl rule list|get|add|test|remove [--dir ./rules-library]

  ComplianceAsCode/content (cac-content) rules — operate on a rule dir or shared.yml:
  celctl cac lint  <rule-dir>                       Compile + empty-eval; catches list/.items bugs
  celctl cac test  <rule-dir> --cases cases.yaml    Unit-test against fixtures
  celctl cac test  <rule-dir> --mock name=data.json [--expect true|false]
  celctl cac live  <rule-dir>                        Evaluate against the cluster (kubectl)

  celctl discover                                  kubectl api-resources
  celctl samples  <resource> [-n ns] [--max N]     Sample objects from cluster

CEL note: inputs are List-wrapped; iterate with <var>.items.all(x, ...) / .exists(...).
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var code int
	switch cmd {
	case "verify", "validate":
		code = cmdVerify(args)
	case "eval":
		code = cmdEval(args)
	case "live":
		code = cmdLive(args)
	case "rule":
		code = cmdRule(args)
	case "cac":
		code = cmdCac(args)
	case "discover":
		code = cmdDiscover(args)
	case "samples":
		code = cmdSamples(args)
	case "-h", "--help", "help":
		usage()
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		code = 2
	}
	os.Exit(code)
}
