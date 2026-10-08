package scripts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const gateVerdict = "✓ check clean"

// repoRoot — корень репо: тесты пакета идут из scripts/.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// run запускает команду без окружения make и git вызывающего (тесты идут и внутри make check, и
// из хука git) и без глобального конфига git: чужие hooksPath и подпись коммитов не мешают.
func run(t *testing.T, dir, stdin string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(key, "GIT_"), strings.HasPrefix(key, "MAKE"), key == "MFLAGS", key == "GNUMAKEFLAGS":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Guard сессии Claude не пускает удобные обходы хуков и не мешает их читать: ложный отказ на
// чтении приучает обходить сам guard.
func TestGuardBash(t *testing.T) {
	guard := filepath.Join(repoRoot(t), ".claude", "hooks", "guard-bash.sh")
	for _, tc := range []struct {
		cmd     string
		blocked bool
	}{
		{"git commit -n -m x", true},
		{"git commit -an -m x", true},
		{"git commit -m x -n", true},
		{`git commit "-n" -m x`, true},
		{"git commit '-an' -m x", true},
		{"git add -A\ngit commit -n -m x", true},
		{"git push --no-verify", true},
		{"git push --no-veri origin main", true},
		{`git push "--no-veri" origin main`, true},
		{"git -c core.hooksPath=/dev/null push", true},
		{"git config core.hooksPath /dev/null", true},
		{"git config --unset core.hooksPath", true},
		{"chmod -x scripts/git-hooks/pre-push", true},
		{"chmod -x scripts/git-hooks/commit-msg", true},
		{"cd scripts/git-hooks && chmod -x commit-msg", true},
		{"rm scripts/git-hooks/pre-push", true},
		{"mv scripts/git-hooks/pre-push /tmp/", true},
		{"echo 'exit 0' > scripts/git-hooks/commit-msg", true},
		{"sed -i 's/make check/true/' scripts/git-hooks/pre-push", true},
		{"cp /bin/true scripts/git-hooks/pre-push", true},
		{"cp -t scripts/git-hooks /tmp/pre-push", true},
		{"sort -o scripts/git-hooks/pre-push /dev/null", true},
		{"find scripts -name pre-push -exec chmod -x {} +", true},
		{"find scripts/git-hooks -name pre-push -delete", true},
		{"find /dev/null -fprint scripts/git-hooks/pre-push", true},
		{"awk -i inplace '{print}' scripts/git-hooks/pre-push", true},
		{`awk 'BEGIN{system("chmod -x scripts/git-hooks/pre-push")}'`, true},
		{"git checkout HEAD~1 -- scripts/git-hooks/commit-msg", true},
		{`echo "a\|b" ; chmod -x scripts/git-hooks/pre-push`, true},
		{"git push origin main", false},
		{"git log -n 5", false},
		{"make hooks", false},
		{`git commit -m "fix: -n в сообщении"`, false},
		{`git commit -m "fix(gate): commit-msg и pre-push"`, false},
		{"git commit -q -F - <<'EOF'\nfix: x\n\ngit commit -n и core.hooksPath в теле\nEOF", false},
		{"grep -rn core.hooksPath scripts/", false},
		{"cat scripts/git-hooks/pre-push", false},
		{"bash -n scripts/git-hooks/commit-msg", false},
		{"scripts/git-hooks/commit-msg /tmp/msg.txt", false},
		{"git diff scripts/git-hooks", false},
		{"git add scripts/git-hooks/commit-msg", false},
		{"find . -name pre-push", false},
		{"awk 'NR<5' scripts/git-hooks/pre-push", false},
		{"sort scripts/git-hooks/commit-msg", false},
		{"cp scripts/git-hooks/pre-push /tmp/pre-push.copy", false},
		{`grep -n "push\|pre-push" README.md`, false},
		{`grep -n "a\;pre-push" README.md`, false},
		{"make check 2>&1 | tee /tmp/pre-push.log", false},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			var input bytes.Buffer
			enc := json.NewEncoder(&input)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": tc.cmd}}); err != nil {
				t.Fatal(err)
			}
			out, err := run(t, "", input.String(), nil, "bash", guard)
			var exit *exec.ExitError
			switch {
			case tc.blocked && (!errors.As(err, &exit) || exit.ExitCode() != 2):
				t.Fatalf("the guard lets a bypass of the git hooks through (err %v): %s", err, out)
			case !tc.blocked && err != nil:
				t.Fatalf("the guard blocks ordinary work: %s", out)
			}
		})
	}
}

// По первой строке /release собирает заметки версии; длина subject — в символах при любой локали.
func TestCommitMsgHook(t *testing.T) {
	hook := filepath.Join(repoRoot(t), "scripts", "git-hooks", "commit-msg")
	long := strings.Repeat("я", 72)
	const footer = "BREAKING CHANGE: NewServer возвращает ошибку — обработать её"
	for _, tc := range []struct {
		head, body string
		ok         bool
	}{
		{"feat(httpkit): дедлайн запроса", "", true},
		{"fix: ключ ошибки логов — error", "", true},
		{"chore(plan): план закрыт", "", true},
		{"refactor(cachekit,natskit): общий хелпер", "", true},
		{"fix(authnkit/session): ротация", "", true},
		{"fix(gate): " + long, "", true},
		{"feat(httpkit)!: NewServer с ошибкой", footer, true},
		{"fix(pgkit): слова BREAKING CHANGE: не с начала строки", "в теле: BREAKING CHANGE: x", true},
		{`Revert "fix(gate): pre-push проверяет пушимый коммит"`, "", true},
		{"fixup! fix(gate): pre-push проверяет пушимый коммит", "", true},
		{"squash! fix(gate): pre-push проверяет пушимый коммит", "", true},
		{"update stuff", "", false},
		{"meta(plan): план закрыт", "", false},
		{"Fix(gate): тип с заглавной", "", false},
		{"fix(gate):без пробела", "", false},
		{"fix(gate): " + long + "я", "", false},
		{"feat(httpkit)!: NewServer с ошибкой", "", false},
		{"feat(httpkit): NewServer с ошибкой", footer, false},
		{"Merge branch 'main' into feature", "", false},
		{"", "", false},
	} {
		for _, locale := range []string{"C", "en_US.UTF-8"} {
			t.Run(locale+"/"+tc.head, func(t *testing.T) {
				msg := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
				write(t, filepath.Dir(msg), map[string]string{"COMMIT_EDITMSG": tc.head + "\n\n" + tc.body + "\n"})
				out, err := run(t, "", "", []string{"LC_ALL=" + locale}, hook, msg)
				if tc.ok != (err == nil) {
					t.Fatalf("commit-msg accepted = %v, want %v: %s", err == nil, tc.ok, out)
				}
			})
		}
	}
}

// Вердикт push — про пушимый коммит: рабочее дерево, которое «чинит» гейт без коммита, на него
// не влияет. Гейт игрушечного репо — make check его Makefile.
func TestPrePushGatesPushedCommit(t *testing.T) {
	hook := filepath.Join(repoRoot(t), "scripts", "git-hooks", "pre-push")
	repo, tmp := t.TempDir(), t.TempDir()
	const gate = "check:\n\t@echo \"toy gate: $$(cat verdict)\"; test \"$$(cat verdict)\" = green\n"
	git := func(args ...string) string {
		t.Helper()
		out, err := run(t, repo, "", nil, "git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	commit := func(color string) string {
		t.Helper()
		write(t, repo, map[string]string{"Makefile": gate, "verdict": color + "\n"})
		git("add", "-A")
		git("commit", "-q", "-m", "test: "+color)
		return git("rev-parse", "HEAD")
	}
	push := func(lines ...string) (string, error) {
		t.Helper()
		return run(t, repo, strings.Join(lines, "\n")+"\n", []string{"TMPDIR=" + tmp}, hook)
	}
	zero := strings.Repeat("0", 40)
	git("init", "-q", "-b", "main")

	red := commit("red")
	write(t, repo, map[string]string{
		"verdict":               "green\n",
		"Makefile":              "check:\n\t@echo forged\n",
		"scripts/gate-clean.sh": "#!/bin/sh\nexit 0\n",
	})
	if err := os.Chmod(filepath.Join(repo, "scripts", "gate-clean.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := push("refs/heads/main " + red + " refs/heads/main " + zero)
	if err == nil || !strings.Contains(out, "toy gate: red") || strings.Contains(out, "forged") {
		t.Fatalf("a red commit is pushed past the gate of the commit (err %v):\n%s", err, out)
	}

	green := commit("green")
	out, err = push(
		"refs/heads/main "+green+" refs/heads/main "+red,
		"refs/tags/v1 "+green+" refs/tags/v1 "+zero,
		"(delete) "+zero+" refs/heads/old "+red,
	)
	if err != nil || strings.Count(out, "toy gate: green") != 1 {
		t.Fatalf("a green commit under two refs must pass with one run of the gate (err %v):\n%s", err, out)
	}
	if list := git("worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Fatalf("the hook left its worktree behind:\n%s", list)
	}
}

// Флаги make и лишний makefile не подделывают вердикт гейта. Настоящий гейт здесь падает на первом
// шаге с golangci-lint — в PATH не пиненная версия, — поэтому проба не гоняет его целиком. Makefile
// под подделку (переприсваивает MAKEFILE_LIST) обходит любую сверку внутри make: это правка гейта.
func TestMakeCheckCannotBeForged(t *testing.T) {
	root := repoRoot(t)
	bin, fakes := t.TempDir(), t.TempDir()
	fail, err := exec.LookPath("false")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fail, filepath.Join(bin, "golangci-lint")); err != nil {
		t.Fatal(err)
	}
	write(t, fakes, map[string]string{
		"colon.mk":  "check gate-clean:\n\t@echo \"" + gateVerdict + "\"\n",
		"double.mk": "check gate-clean::\n\t@echo \"" + gateVerdict + "\"\n",
		"shell.mk":  "override SHELL := /bin/true\n",
	})
	colon, double, shell := filepath.Join(fakes, "colon.mk"), filepath.Join(fakes, "double.mk"), filepath.Join(fakes, "shell.mk")
	for _, tc := range []struct {
		name string
		args []string
		env  []string
		gate bool // настоящий гейт запускается (и падает на заглушке)
	}{
		{name: "make check", args: []string{"check"}, gate: true},
		{name: "-f ./Makefile", args: []string{"-f", "./Makefile", "check"}, gate: true},
		{name: "second makefile with check:", args: []string{"-f", "Makefile", "-f", colon, "check"}},
		{name: "-o and second makefile", args: []string{"-o", "gate-makefiles", "-f", "Makefile", "-f", colon, "check"}},
		{name: "second makefile with check:: after", args: []string{"-f", "Makefile", "-f", double, "check"}},
		{name: "second makefile with check:: before", args: []string{"-f", double, "-f", "Makefile", "check"}},
		{name: "second makefile with SHELL", args: []string{"-f", "Makefile", "-f", shell, "check"}},
		{name: "MAKEFILES", args: []string{"check"}, env: []string{"MAKEFILES=" + double}},
		{name: "--eval", args: []string{"--eval", "SHELL:=/bin/true", "check"}},
		{name: "-E rule", args: []string{"-E", "check:: ; @echo \"" + gateVerdict + "\"", "check"}},
		{name: "-k", args: []string{"-k", "-f", "Makefile", "-f", double, "check"}},
		{name: "-p", args: []string{"-p", "check"}},
		{name: "-i", args: []string{"-i", "check"}},
		{name: "-n", args: []string{"-n", "check"}},
		{name: "gate-clean: second makefile after", args: []string{"-f", "Makefile", "-f", double, "gate-clean"}},
		{name: "gate-clean: second makefile before", args: []string{"-f", double, "-f", "Makefile", "gate-clean"}},
		{name: "gate-clean: -o and second makefile", args: []string{"-o", "gate-makefiles", "-f", "Makefile", "-f", colon, "gate-clean"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}, tc.env...)
			out, err := run(t, root, "", env, "make", tc.args...)
			if err == nil || strings.Contains(out, gateVerdict) {
				t.Fatalf("make %v forges the verdict of the gate (err %v):\n%s", tc.args, err, out)
			}
			if tc.gate && !strings.Contains(out, "→ hooks-check") {
				t.Fatalf("make %v must run the real gate:\n%s", tc.args, out)
			}
			// До fmt-check гейт доходит с активными хуками (make hooks); дальше его не пускает версия тула.
			if strings.Contains(out, "→ fmt-check") && !strings.Contains(out, "пиненную версию") {
				t.Fatalf("make %v must stop at the tool that is not the pinned version:\n%s", tc.args, out)
			}
		})
	}
}

// Линтер, снятый из enable, и опечатка в узле конфига (golangci-lint run молча теряет его
// настройки) не оставляют следа в отчёте «0 issues» — их ловит сам шаг lint до прогона.
func TestLintRefusesWeakenedConfig(t *testing.T) {
	root := repoRoot(t)
	config, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	etalon, err := os.ReadFile(filepath.Join(root, "scripts", "linters.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, old, repl, want string
	}{
		{"linter dropped", "\n    - errcheck\n", "\n", "снят: errcheck"},
		{"linter added", "\n    - errcheck\n", "\n    - errcheck\n    - gochecknoglobals\n", "вне эталона: gochecknoglobals"},
		{"formatter replaced", "\n    - gofumpt\n", "\n    - gofmt\n", "снят: gofumpt"},
		{"misplaced node", "  settings:\n    errorlint:\n", "  setings:\n    errorlint:\n", "jsonschema"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(string(config), tc.old) != 1 {
				t.Fatalf("anchor %q: want one match in .golangci.yml", tc.old)
			}
			dir := t.TempDir()
			write(t, dir, map[string]string{
				".golangci.yml":       strings.Replace(string(config), tc.old, tc.repl, 1),
				"scripts/linters.txt": string(etalon),
			})
			out, err := run(t, dir, "", nil, "bash", filepath.Join(root, "scripts", "lint.sh"))
			if err == nil || !strings.Contains(out, tc.want) {
				t.Fatalf("lint must refuse the config with %q (err %v):\n%s", tc.want, err, out)
			}
		})
	}
}
