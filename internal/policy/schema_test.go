package policy

import (
	"errors"
	"strings"
	"testing"
)

func TestSchemaErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		line int    // expected line of the (first) error
		msg  string // substring of the error
	}{
		{"unknown top key", "version: 1\nrulez: []\n", 2, `unknown key "rulez" in top level (did you mean "rules"?)`},
		{"unknown rule key", "version: 1\nrules:\n  - name: x\n    match: {tool: Bash}\n    acton: deny\n", 5, `did you mean "action"?`},
		{"unknown match key", "version: 1\nrules:\n  - name: x\n    match:\n      paths: x\n    action: deny\n", 5, `unknown key "paths" in rule "x" match (did you mean "path"?)`},
		{"unknown key no suggestion", "version: 1\nrules:\n  - name: x\n    match: {tool: Bash}\n    action: deny\n    zzzzzzzz: 1\n", 6, "valid keys: name, description"},
		{"bad action", "version: 1\nrules:\n  - name: x\n    match: {tool: Bash}\n    action: block\n", 5, `action must be allow, ask or deny, not "block"`},
		{"missing version", "rules: []\n", 1, "missing `version: 1`"},
		{"wrong version", "version: 2\nrules: []\n", 1, `unsupported version "2"`},
		{"rules not list", "version: 1\nrules: {a: b}\n", 2, "`rules:` must be a list"},
		{"duplicate name", "version: 1\nrules:\n  - {name: x, match: {tool: A}, action: deny}\n  - {name: x, match: {tool: B}, action: deny}\n", 4, `duplicate rule name "x" (first defined on line 3)`},
		{"reserved name", "version: 1\nrules:\n  - {name: askgate-mine, match: {tool: A}, action: deny}\n", 3, "reserved for built-in rules"},
		{"missing name", "version: 1\nrules:\n  - {match: {tool: A}, action: deny}\n", 3, "rule 1: missing `name:`"},
		{"missing action", "version: 1\nrules:\n  - {name: x, match: {tool: A}}\n", 3, `rule "x": missing ` + "`action:`"},
		{"no match or when", "version: 1\nrules:\n  - {name: x, action: deny}\n", 3, "needs `match:` and/or `when:`"},
		{"empty match", "version: 1\nrules:\n  - {name: x, match: {}, action: deny}\n", 3, "`match:` is empty"},
		{"bad kind", "version: 1\nrules:\n  - name: x\n    match: {kind: bash}\n    action: deny\n", 4, `unknown kind "bash"`},
		{"bad agent in match", "version: 1\nrules:\n  - name: x\n    match:\n      agent: gpt\n    action: deny\n", 5, `unknown agent "gpt"`},
		{"bad agents list", "version: 1\nrules:\n  - name: x\n    match: {tool: A}\n    agents: [claude, cursor]\n    action: deny\n", 5, `unknown agent "cursor"`},
		{"cel syntax", "version: 1\nrules:\n  - name: x\n    match: {tool: A}\n    when: 'command.startsWith('\n    action: deny\n", 5, `rule "x": when:`},
		{"cel unknown var", "version: 1\nrules:\n  - name: x\n    when: 'comand == \"x\"'\n    action: deny\n", 4, "undeclared reference to 'comand'"},
		{"cel not bool", "version: 1\nrules:\n  - name: x\n    when: '1 + 2'\n    action: deny\n", 4, "must evaluate to a bool"},
		{"empty when", "version: 1\nrules:\n  - name: x\n    when: ''\n    action: deny\n", 4, "`when:` is empty"},
		{"duplicate key", "version: 1\nrules:\n  - name: x\n    name: y\n    match: {tool: A}\n    action: deny\n", 4, `duplicate key "name"`},
		{"yaml syntax", "version: 1\nrules:\n  - name: x\n   bad: [\n", -1, "did not find"},
		{"yaml tab", "version: 1\nrules:\n\t- name: x\n", 3, "tab"},
		{"match not mapping", "version: 1\nrules:\n  - {name: x, match: Bash, action: deny}\n", 3, "`match:` must be a mapping"},
		{"empty list", "version: 1\nrules:\n  - {name: x, match: {tool: []}, action: deny}\n", 3, "match.tool is an empty list"},
		{"top not mapping", "- a\n- b\n", 1, "expected a mapping"},
		{"rule not mapping", "version: 1\nrules:\n  - just a string\n", 3, "rule 1: expected a mapping"},
		{"bad glob", "version: 1\nrules:\n  - {name: x, match: {command: \"{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}\"}, action: deny}\n", 3, "more than 64 alternatives"},
		{"name whitespace", "version: 1\nrules:\n  - {name: ' x', match: {tool: A}, action: deny}\n", 3, "leading/trailing spaces"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compilePolicy("p.yaml", []byte(tt.src), "/home/u")
			if err == nil {
				t.Fatal("expected an error")
			}
			var fe FileErrors
			if !errors.As(err, &fe) || len(fe) == 0 {
				t.Fatalf("error is %T, want FileErrors", err)
			}
			if (tt.line < 0 && fe[0].Line <= 0) || (tt.line >= 0 && fe[0].Line != tt.line) {
				t.Errorf("line = %d, want %d (%v)", fe[0].Line, tt.line, err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("error %q does not contain %q", err, tt.msg)
			}
			if !strings.HasPrefix(err.Error(), "p.yaml:") {
				t.Errorf("error %q lacks the file name", err)
			}
		})
	}
}

func TestSchemaReportsAllErrors(t *testing.T) {
	src := `version: 1
rules:
  - name: a
    match: {kind: bash}
    action: deny
  - name: b
    match: {tool: X}
    action: nope
`
	_, err := compilePolicy("p.yaml", []byte(src), "")
	var fe FileErrors
	if !errors.As(err, &fe) || len(fe) != 2 {
		t.Fatalf("want 2 errors, got %v", err)
	}
	if fe[0].Line != 4 || fe[1].Line != 8 {
		t.Errorf("lines = %d, %d; want 4, 8", fe[0].Line, fe[1].Line)
	}
}

func TestSchemaValid(t *testing.T) {
	src := `# comment
version: 1
rules:
  - name: list-values
    description: lists are any-of
    match:
      tool: [Write, Edit]
      path: ["*.pem", "~/.ssh/**"]
      agent: claude
      kind: "{edit,other}"
    agents: [claude, codex]
    action: deny
    message: no
  - name: cel-only
    when: 'has_word(command, "rm") && glob(path, "**/x") && command.lowerAscii().contains("rm")'
    action: ask
  - name: anchors
    match: &m {tool: Bash}
    action: allow
  - name: alias
    match: *m
    action: allow
`
	rules, err := compilePolicy("p.yaml", []byte(src), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("got %d rules", len(rules))
	}
	if rules[0].line != 4 || rules[0].description != "lists are any-of" || len(rules[0].agents) != 2 {
		t.Errorf("rule 0 = %+v", rules[0])
	}
}

func TestSchemaEmpty(t *testing.T) {
	for _, src := range []string{"", "# only a comment\n", "\n\n"} {
		rules, err := compilePolicy("p.yaml", []byte(src), "")
		if err != nil || len(rules) != 0 {
			t.Errorf("%q: rules=%d err=%v", src, len(rules), err)
		}
	}
	if rules, err := compilePolicy("p.yaml", []byte("version: 1\n"), ""); err != nil || len(rules) != 0 {
		t.Errorf("version only: %v", err)
	}
}

func TestSchemaTooLarge(t *testing.T) {
	_, err := compilePolicy("p.yaml", make([]byte, maxPolicyBytes+1), "")
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}

func TestStarterPolicyCompiles(t *testing.T) {
	n, err := Check("starter.yaml", []byte(StarterPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if n < 7 {
		t.Fatalf("starter has %d rules", n)
	}
	// The commented-out examples must compile too once uncommented.
	var b strings.Builder
	for _, l := range strings.Split(StarterPolicy, "\n") {
		if strings.HasPrefix(l, "  # ") && !strings.HasPrefix(l, "  # ---") {
			l = "  " + strings.TrimPrefix(l, "  # ")
		} else if l == "  #" {
			l = ""
		}
		b.WriteString(l + "\n")
	}
	if n2, err := Check("starter-uncommented.yaml", []byte(b.String())); err != nil || n2 <= n {
		t.Fatalf("uncommented starter: n=%d err=%v", n2, err)
	}
}

func TestSuggest(t *testing.T) {
	if s := suggest("acton", ruleKeys); s != "action" {
		t.Errorf("suggest = %q", s)
	}
	if s := suggest("completely-different", ruleKeys); s != "" {
		t.Errorf("suggest = %q", s)
	}
}
