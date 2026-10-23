package codex

import (
	"errors"
	"strings"
	"testing"
)

const sampleConfig = `# Codex config
model = "gpt-5.5-codex"   # inline comment
approval_policy = "on-request"
notify = ["/usr/local/bin/notify", "--flag"]
multi = """
line one "quoted"
[not.a.table]
"""
lit = '''raw [x]'''
tbl = { a = 1, b = "two", nested = { c = [1, 2] } }

[features]
hooks = true

[mcp_servers.github]
command = "gh-mcp"
args = [
  "serve", # comment inside array
  "--stdio",
]

[projects."/home/user/my project"]
trust_level = "trusted"

[hooks.state."/home/user/.codex/hooks.json:stop:0:0"]
trusted_hash = "sha256:aaa"
enabled = true
`

func TestParseTOML(t *testing.T) {
	doc, err := parseTOML(sampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	var headers []string
	for _, tb := range doc.tables {
		headers = append(headers, strings.Join(tb.path, "|"))
	}
	want := "features mcp_servers|github projects|/home/user/my project hooks|state|/home/user/.codex/hooks.json:stop:0:0"
	if strings.Join(headers, " ") != want {
		t.Errorf("tables = %q", headers)
	}
	if v, _ := doc.stringAt("model"); v != "gpt-5.5-codex" {
		t.Errorf("model = %q", v)
	}
	if v, _ := doc.stringAt("hooks", "state", "/home/user/.codex/hooks.json:stop:0:0", "trusted_hash"); v != "sha256:aaa" {
		t.Errorf("trusted_hash = %q", v)
	}
	if v, _ := doc.rawAt("features", "hooks"); v != "true" {
		t.Errorf("features.hooks = %q", v)
	}
	if hooksFeatureOff(doc) {
		t.Error("feature reported off")
	}
	if v, _ := doc.stringAt("projects", "/home/user/my project", "trust_level"); v != "trusted" {
		t.Errorf("quoted table key: %q", v)
	}
}

func TestParseTOMLVariants(t *testing.T) {
	for _, src := range []string{
		"",
		"\n\n# only comments\n",
		"a = 1\r\nb = 'x'\r\n[t]\r\nc = true\r\n",
		`s = "esc \" \\ \u00e9 \U0001F600"` + "\n",
		"dotted.key . sub = 1\n",
		"'lit key' = 2\n",
		"arr = [ [1, 2], { a = 1 }, \"]\" ]\n",
		"[[array.tables]]\nx = 1\n[[array.tables]]\nx = 2\n",
		"t = { a = 1,\n  b = 2 }\n",
		"ml = \"\"\"a\"\"\"\"\" # five quotes close with two in content\n",
		"[ spaced . header ] # c\n",
		"no_newline_at_end = 1",
	} {
		if _, err := parseTOML(src); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
	doc, _ := parseTOML(`s = "esc \" \\ \u00e9"`)
	if v, _ := doc.stringAt("s"); v != `esc " \ é` {
		t.Errorf("escapes decoded to %q", v)
	}
	doc, _ = parseTOML("[features]\ncodex_hooks = false\n")
	if !hooksFeatureOff(doc) {
		t.Error("codex_hooks = false not detected")
	}
	for _, bad := range []string{"a = \n", "a = \"open\n", "[t\n", "= 1\n", "a = 1 b = 2\n", "a = [1, 2\n", "a = \"\"\"never closed\n", "a = \"\\q\"\n"} {
		if _, err := parseTOML(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestApplyTrustAddsAndKeepsComments(t *testing.T) {
	edit := trustEdit{want: map[string]string{
		"/h/hooks.json:pre_tool_use:0:0": "sha256:p",
		"/h/hooks.json:stop:0:0":         "sha256:s",
	}}
	out, err := applyTrust(sampleConfig, edit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, sampleConfig) {
		t.Fatalf("existing content changed:\n%s", out)
	}
	added := out[len(sampleConfig):]
	want := "\n" + markerComment + "\n[hooks.state.\"/h/hooks.json:pre_tool_use:0:0\"]\ntrusted_hash = \"sha256:p\"\n" +
		"\n" + markerComment + "\n[hooks.state.\"/h/hooks.json:stop:0:0\"]\ntrusted_hash = \"sha256:s\"\n"
	if added != want {
		t.Errorf("added:\n%s", added)
	}
	if _, err := parseTOML(out); err != nil {
		t.Fatalf("output is not TOML: %v", err)
	}
	again, err := applyTrust(out, edit)
	if err != nil || again != out {
		t.Errorf("not idempotent:\n%s", again)
	}
	// Uninstall: our entries go, everything else stays byte-identical.
	back, err := applyTrust(out, trustEdit{stale: map[string]bool{"/h/hooks.json:pre_tool_use:0:0": true}})
	if err != nil {
		t.Fatal(err)
	}
	if back != sampleConfig {
		t.Errorf("uninstall did not restore:\n%q\nwant\n%q", back, sampleConfig)
	}
}

func TestApplyTrustEmptyAndMissingNewline(t *testing.T) {
	edit := trustEdit{want: map[string]string{"k": "sha256:1"}}
	out, err := applyTrust("", edit)
	if err != nil {
		t.Fatal(err)
	}
	if out != markerComment+"\n[hooks.state.k]\ntrusted_hash = \"sha256:1\"\n" {
		t.Errorf("empty file: %q", out)
	}
	out, _ = applyTrust(`model = "x"`, edit)
	if !strings.HasPrefix(out, "model = \"x\"\n\n# askgate-managed") {
		t.Errorf("no trailing newline: %q", out)
	}
	if back, _ := applyTrust(out, trustEdit{}); back != "model = \"x\"\n" {
		t.Errorf("marker-based removal: %q", back)
	}
}

func TestApplyTrustRenamesForeignAndDropsStale(t *testing.T) {
	src := `[hooks.state."/h:pre_tool_use:0:0"] # ours, old position
trusted_hash = "sha256:old"

# the user's lint hook
[hooks.state."/h:pre_tool_use:1:0"] # lint
trusted_hash = "sha256:lint"

[hooks.state."/h:stop:0:0"]
trusted_hash = "sha256:orphan"
`
	edit := trustEdit{
		stale:  map[string]bool{"/h:pre_tool_use:0:0": true},
		rename: map[string]string{"/h:pre_tool_use:1:0": "/h:pre_tool_use:0:0"},
		want:   map[string]string{"/h:pre_tool_use:1:0": "sha256:new"},
	}
	out, err := applyTrust(src, edit)
	if err != nil {
		t.Fatal(err)
	}
	want := `# the user's lint hook
[hooks.state."/h:pre_tool_use:0:0"] # lint
trusted_hash = "sha256:lint"

[hooks.state."/h:stop:0:0"]
trusted_hash = "sha256:orphan"

` + markerComment + `
[hooks.state."/h:pre_tool_use:1:0"]
trusted_hash = "sha256:new"
`
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestApplyTrustDropsOrphanAtRenameTarget(t *testing.T) {
	src := "[hooks.state.\"/h:stop:0:0\"]\ntrusted_hash = \"sha256:orphan\"\n\n[hooks.state.\"/h:stop:1:0\"]\ntrusted_hash = \"sha256:user\"\n"
	out, err := applyTrust(src, trustEdit{rename: map[string]string{"/h:stop:1:0": "/h:stop:0:0"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "[hooks.state.\"/h:stop:0:0\"]\ntrusted_hash = \"sha256:user\"\n" {
		t.Errorf("got %q", out)
	}
}

func TestApplyTrustRefusesUnsupportedLayouts(t *testing.T) {
	edit := trustEdit{want: map[string]string{"k": "sha256:1"}}
	for _, src := range []string{
		"hooks = { state = {} }\n",
		"[hooks]\nstate = { k = { trusted_hash = \"x\" } }\n",
		"[hooks]\nstate.k.trusted_hash = \"x\"\n",
		"[hooks.state]\nk = { trusted_hash = \"x\" }\n",
		"[[hooks]]\nx = 1\n",
		"[hooks.state.k.deeper]\nx = 1\n",
	} {
		_, err := applyTrust(src, edit)
		var ue ErrUnsupportedLayout
		if !errors.As(err, &ue) {
			t.Errorf("%q: err = %v", src, err)
		}
	}
	// Other keys under [hooks.state] in dotted form are fine.
	if _, err := applyTrust("[hooks.state]\nother = { trusted_hash = \"x\" }\n", edit); err != nil {
		t.Errorf("unrelated dotted key refused: %v", err)
	}
	if _, err := applyTrust("a = \"unterminated\n", edit); err == nil {
		t.Error("invalid TOML accepted")
	}
}

func TestRenameHeaderKeepsComment(t *testing.T) {
	got := renameHeader(`  [ hooks.state."a" ]   # keep me`, "b c")
	if got != `  [hooks.state."b c"]   # keep me` {
		t.Errorf("got %q", got)
	}
}
