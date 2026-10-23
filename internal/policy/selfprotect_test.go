package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xasdta/askgate/internal/api"
)

func TestSelfProtectionShell(t *testing.T) {
	te := newTestEnv(t)
	// An allow-everything user rule must not silence the built-in ask.
	writeFile(t, te.userFile(), "version: 1\nrules:\n  - {name: yolo, match: {tool: \"*\"}, action: allow}\n")
	e, _ := te.load(t)
	sock := filepath.Join(te.run, "askgate.sock")

	tests := []struct {
		cmd  string
		want bool
	}{
		// --- admin subcommands, however they are spelled -------------------
		{"askgate approve r_123", true},
		{"./askgate approve r_1", true},
		{"/usr/local/bin/askgate deny r_1", true},
		{"ASKGATE approve r_1", true}, // case-insensitive filesystems
		{`"askgate" approve r_1`, true},
		{`ask''gate approve r_1`, true},
		{`a\skgate approve r_1`, true},
		{`$'\x61skgate' approve r_1`, true},
		{"askgate --verbose remote off", true},
		{"askgate pair", true},
		{"askgate devices revoke d_1", true},
		{"askgate service install", true},
		{"askgate hooks uninstall", true},
		{"askgate here", true},
		{"askgate away", true},
		{"askgate auto", true},
		{"askgate stop", true},
		{"askgate open", true},
		{"askgate setup", true},
		{"askgate policy init --force", true},
		{"askgate policy trust .", true},
		{"askgate policy untrust", true},
		{"askgate policy", true},
		// --- wrappers and chains --------------------------------------------
		{"env X=1 askgate approve r_1", true},
		{"env -i PATH=/bin askgate here", true},
		{"env -u FOO askgate away", true},
		{`env -S "askgate approve r_1"`, true},
		{"X=1 Y=2 askgate pair", true},
		{"sudo -u me askgate service install", true},
		{"nohup askgate stop &", true},
		{"timeout 5 askgate hooks uninstall", true},
		{"nice -n 10 askgate approve r_1", true},
		{"time askgate approve r_1", true},
		{"command askgate approve r_1", true},
		{"ls; askgate approve r_1", true},
		{"true && askgate away", true},
		{"false || askgate away", true},
		{"echo y | askgate approve r_1", true},
		{"(askgate approve r_1)", true},
		{"{ askgate approve r_1; }", true},
		{"if true; then askgate approve r_1; fi", true},
		{"for i in 1; do askgate approve r_$i; done", true},
		{"cd /tmp &&\naskgate approve r_1", true},
		{"ls \\\n && askgate approve r_1", true},
		// --- nested shells and substitutions ---------------------------------
		{`sh -c "askgate approve r_1"`, true},
		{`bash -lc 'askgate policy trust .'`, true},
		{`zsh -c 'cd x; askgate deny r_1'`, true},
		{`bash -c "bash -c 'askgate approve r_1'"`, true},
		{`echo $(askgate approve r_1)`, true},
		{"echo `askgate approve r_1`", true},
		{`echo "$(askgate approve r_1)"`, true},
		{`cat <(askgate approve r_1)`, true},
		{`eval "askgate approve r_1"`, true},
		{`bash <<< "askgate approve r_1"`, true},
		{`echo "askgate approve r_1" | sh`, true},
		{`printf 'askgate approve r_1' | bash -s`, true},
		{`python3 -c "import os; os.system('askgate approve r_1')"`, true},
		{`osascript -e 'do shell script "askgate approve r_1"'`, true},
		{`echo r_1 | xargs askgate approve`, true},
		{`echo approve r_1 | xargs askgate`, true},
		{`find . -name x -exec askgate approve {} \;`, true},
		{"askgate run -- askgate approve r_1", true},
		{"askgate run -n x -- sh -c 'askgate deny r_1'", true},
		{`AG=askgate; $AG approve r_1`, true},
		{`busybox sh -c "askgate approve r_1"`, true},
		// --- writes to protected paths ----------------------------------------
		{"echo x > ~/.askgate/policy.yaml", true},
		{"echo x >> $HOME/.askgate/config.yaml", true},
		{"echo x >| ${HOME}/.askgate/config.yaml", true},
		{"cat x 2> ~/.askgate/x", true},
		{"echo x &> ~/.askgate/x", true},
		{"echo x>~/.askgate/x", true},
		{`sed -i '' s/a/b/ ~/.askgate/policy.yaml`, true},
		{`sed -i.bak s/a/b/ ~/.askgate/policy.yaml`, true},
		{`sed -ni s/a/b/p ~/.askgate/policy.yaml`, true},
		{"echo x >& ~/.askgate/x", true},
		{"tee ~/.claude/settings.json < x", true},
		{"tee -a ~/.codex/config.toml", true},
		{"cp /tmp/x ~/.askgate/trusted-repos.yaml", true},
		{"mv x .claude/settings.local.json", true}, // relative to the repo (cwd)
		{"rm .askgate/policy.yaml", true},          // the repo's own policy
		{"cd ~/.askgate && rm policy.yaml", true},
		{"cd && rm -rf .askgate", true},
		{`rm -rf "$ASKGATE_HOME"`, true},
		{`sqlite3 ~/.askgate/askgate.db "update requests set status='decided'"`, true},
		{"curl --unix-socket " + sock + " -X POST http://x/v1/requests/r_1/decide", true},
		{"sort -o ~/.askgate/policy.yaml x", true},
		{"find ~/.askgate -name '*.yaml' -delete", true},
		{"dd if=/dev/zero of=" + te.agHome + "/askgate.db", true},
		{"ln -sf /tmp/evil ~/.askgate/policy.yaml", true},
		{"chmod 777 ~/.askgate", true},
		{`python3 -c "open('` + te.agHome + `/policy.yaml','w').write('')"`, true},
		{`node -e "require('fs').writeFileSync(process.env.HOME+'/.askgate/policy.yaml','')"`, false}, // computed path: documented limit
		{"vim ~/.askgate/policy.yaml", true},
		{"yq -i '.rules = []' ~/.askgate/policy.yaml", true},

		// --- must NOT trigger --------------------------------------------------
		// Text that merely mentions askgate is not an invocation (decision
		// documented in POLICY.md); it is only scanned when fed to an interpreter.
		{"echo askgate approve r_1", false},
		{`echo "askgate approve r_1" > notes.txt`, false},
		{`git commit -m "askgate approve: fix race"`, false},
		{`grep -rn "askgate approve" docs/`, false},
		{"askgate ls", false},
		{"askgate log --grep approve", false},
		{"askgate status", false},
		{"askgate explain s_1", false},
		{"askgate", false},
		{"askgate version", false},
		{"askgate policy test --command 'askgate approve r_1'", false},
		{"askgate policy check", false},
		{"askgate policy status", false},
		{"askgate run -- claude", false},
		{"askgate-dev approve r_1", false},
		{"myaskgate approve r_1", false},
		{"cat ~/.askgate/policy.yaml", false},
		{"ls -la ~/.askgate", false},
		{"grep approve ~/.askgate/logs/daemon.log", false},
		{"sed s/a/b/ ~/.askgate/policy.yaml", false},
		{"find ~/.askgate -name '*.yaml'", false},
		{"go test ./...", false},
		{"rm -rf node_modules", false},
		{"echo hi > /tmp/out.txt", false},
		{"cp ~/.askgatex/a /tmp/b", false}, // prefix of a protected path is not inside it
		{"echo 2>&1", false},
		{"cat ~/.askgate/policy.yaml 2>&1 | head", false},
		{"sed -n 1,5p ~/.askgate/policy.yaml", false},
		{"sort -u ~/.askgate/x", false},
		{"bash scripts/build.sh", false},
		{"python3 -c 'print(1)'", false},
		{"", false},
	}
	for _, tt := range tests {
		in := shell(tt.cmd)
		in.Cwd, in.Repo = te.repo, te.repo
		r := e.Evaluate(in)
		got := r.Rule == SelfProtectionRule
		if got != tt.want {
			t.Errorf("%q: self-protection=%v, want %v (%s)", tt.cmd, got, tt.want, r.Message)
			continue
		}
		if got && (r.Action != api.BehaviorAsk || r.File != BuiltinFile || !strings.HasPrefix(r.Message, "Askgate self-protection: this ")) {
			t.Errorf("%q: result %+v", tt.cmd, r)
		}
		if !got && r.Action != api.BehaviorAllow && tt.cmd != "" {
			t.Errorf("%q: the allow-all rule should decide, got %+v", tt.cmd, r)
		}
	}
}

func TestSelfProtectionDenyStillWins(t *testing.T) {
	te := newTestEnv(t)
	writeFile(t, te.userFile(), "version: 1\nrules:\n  - {name: no-askgate, match: {command: \"*askgate*\"}, action: deny}\n")
	e, _ := te.load(t)
	r := e.Evaluate(shell("askgate approve r_1"))
	if r.Action != api.BehaviorDeny || r.Rule != "no-askgate" || len(r.Matched) != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestSelfProtectionEditTools(t *testing.T) {
	te := newTestEnv(t)
	e, _ := te.load(t)
	link := filepath.Join(te.home, "innocent")
	if err := os.Symlink(te.agHome, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		kind  api.ToolKind
		paths []string
		cwd   string
		want  bool
	}{
		{"policy file", api.ToolEdit, []string{"~/.askgate/policy.yaml"}, "", true},
		{"absolute", api.ToolEdit, []string{te.agHome + "/config.yaml"}, "", true},
		{"agent settings", api.ToolEdit, []string{te.home + "/.claude/settings.json"}, "", true},
		{"codex config", api.ToolEdit, []string{"~/.codex/config.toml"}, "", true},
		{"repo policy relative", api.ToolEdit, []string{".askgate/policy.yaml"}, te.repo, true},
		{"project claude settings", api.ToolEdit, []string{".claude/settings.local.json"}, te.repo, true},
		{"dot-dot escape", api.ToolEdit, []string{"../../.askgate/policy.yaml"}, te.repo, true},
		{"via symlink", api.ToolEdit, []string{link + "/policy.yaml"}, "", true},
		{"new file via symlinked dir", api.ToolEdit, []string{link + "/new.yaml"}, "", true},
		{"one of many", api.ToolEdit, []string{"/tmp/a", "~/.askgate/x"}, "", true},
		{"mcp writer", api.ToolMCP, []string{"~/.askgate/x"}, "", true},
		{"other tool", api.ToolOther, []string{"~/.askgate/x"}, "", true},
		{"read is fine", api.ToolRead, []string{"~/.askgate/policy.yaml"}, "", false},
		{"web is fine", api.ToolWeb, []string{"~/.askgate/policy.yaml"}, "", false},
		{"source file", api.ToolEdit, []string{"src/main.go"}, te.repo, false},
		{"claude dir other file", api.ToolEdit, []string{".claude/commands/x.md"}, te.repo, false},
		{"lookalike", api.ToolEdit, []string{"~/.askgate-notes.md"}, "", false},
	}
	for _, tt := range tests {
		r := e.Evaluate(Input{Agent: api.AgentClaude, Tool: "Edit", ToolKind: tt.kind, Paths: tt.paths, Cwd: tt.cwd, Repo: tt.cwd})
		if got := r.Rule == SelfProtectionRule; got != tt.want {
			t.Errorf("%s: got %v want %v (%+v)", tt.name, got, tt.want, r)
		}
	}
	if runtime.GOOS == "darwin" {
		r := e.Evaluate(Input{ToolKind: api.ToolEdit, Paths: []string{"~/.ASKGATE/policy.yaml"}})
		if r.Rule != SelfProtectionRule {
			t.Error("case-insensitive filesystem: ~/.ASKGATE must be protected")
		}
	}
}

func TestParseShell(t *testing.T) {
	p := parseShell(`A=1 echo "a b" 'c d' e\ f >out 2>>err | grep -v "$(id)" && x <<< 'here'; y &`)
	if len(p.cmds) != 4 {
		t.Fatalf("cmds = %+v", p.cmds)
	}
	c0 := p.cmds[0]
	if strings.Join(c0.words, "|") != "A=1|echo|a b|c d|e f" {
		t.Errorf("words = %q", c0.words)
	}
	if len(c0.redirs) != 2 || c0.redirs[0] != (shRedir{">", "out"}) || c0.redirs[1] != (shRedir{">>", "err"}) {
		t.Errorf("redirs = %+v", c0.redirs)
	}
	if !p.cmds[1].pipedIn || p.cmds[2].pipedIn {
		t.Errorf("pipes: %+v", p.cmds)
	}
	if p.cmds[2].redirs[0] != (shRedir{"<<<", "here"}) {
		t.Errorf("here-string: %+v", p.cmds[2].redirs)
	}
	if len(p.subs) != 1 || p.subs[0] != "id" {
		t.Errorf("subs = %q", p.subs)
	}
	// Unterminated constructs must not panic or loop.
	for _, s := range []string{`"abc`, `'abc`, "`abc", "$(abc", "$'abc", `\`, "<(x", "a >", "$'\\x", "$'\\1"} {
		parseShell(s)
	}
}

func FuzzSelfProtection(f *testing.F) {
	for _, s := range []string{"askgate approve r_1", `sh -c "x"`, "echo $(a `b` <(c))", "a > b", `$'\x41'`} {
		f.Add(s)
	}
	te := newTestEnv(f)
	e, _ := te.load(f)
	f.Fuzz(func(t *testing.T, cmd string) {
		e.Evaluate(shell(cmd)) // must not panic or hang
	})
}

func BenchmarkSelfProtectionShell(b *testing.B) {
	te := newTestEnv(b)
	e, _ := te.load(b)
	in := shell(`cd /Users/me/src/app && git add -A && git commit -m "fix: handle empty input" && git push origin feature/x 2>&1 | tail -5`)
	b.ReportAllocs()
	for b.Loop() {
		e.Evaluate(in)
	}
}
