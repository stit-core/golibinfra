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
// чтении приучает обходить сам guard. Случаи — testdata/guard-cases.txt: guard получает команду
// как JSON и ничего не исполняет.
func TestGuardBash(t *testing.T) {
	guard := filepath.Join(repoRoot(t), ".claude", "hooks", "guard-bash.sh")
	data, err := os.ReadFile(filepath.Join("testdata", "guard-cases.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cases := 0
	for n, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verdict, cmd, ok := strings.Cut(line, " ")
		if !ok || (verdict != "A" && verdict != "B") {
			t.Fatalf("guard-cases.txt:%d: want \"A <command>\" or \"B <command>\", got %q", n+1, line)
		}
		cases++
		blocked := verdict == "B"
		cmd = strings.NewReplacer("⏎", "\n", "⇥", "\t").Replace(cmd)
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			var input bytes.Buffer
			enc := json.NewEncoder(&input)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": cmd}}); err != nil {
				t.Fatal(err)
			}
			out, err := run(t, "", input.String(), nil, "bash", guard)
			var exit *exec.ExitError
			switch {
			case blocked && (!errors.As(err, &exit) || exit.ExitCode() != 2):
				t.Fatalf("guard-cases.txt:%d: the guard lets a bypass of the git hooks through (err %v): %s", n+1, err, out)
			case !blocked && err != nil:
				t.Fatalf("guard-cases.txt:%d: the guard blocks ordinary work: %s", n+1, out)
			}
		})
	}
	if cases < 300 {
		t.Fatalf("guard-cases.txt: %d cases — the list is cut", cases)
	}
}

// По первой строке /release собирает заметки версии; длина subject — в символах при любой локали.
// Сообщение — файл целиком, как его получает хук: с комментариями редактора и пустыми строками.
func TestCommitMsgHook(t *testing.T) {
	hook := filepath.Join(repoRoot(t), "scripts", "git-hooks", "commit-msg")
	long := strings.Repeat("я", 72)
	const footer = "\n\nBREAKING CHANGE: NewServer возвращает ошибку — обработать её\n"
	for _, tc := range []struct {
		msg string
		ok  bool
	}{
		{"feat(httpkit): дедлайн запроса\n", true},
		{"fix: ключ ошибки логов — error\n\nтело\n", true},
		{"chore(plan): план закрыт\n", true},
		{"refactor(cachekit,natskit): общий хелпер\n", true},
		{"fix(authnkit/session): ротация\n", true},
		{"fix(gate): " + long + "\n", true},
		{"fix(gate): " + long + "  \r\n", true},
		{"feat(httpkit)!: NewServer с ошибкой" + footer, true},
		{"fix(pgkit): слова BREAKING CHANGE: не с начала строки\n\nв теле: BREAKING CHANGE: x\n", true},
		{"fix(gate): x\n\n# BREAKING CHANGE: в комментарии редактора\n", true},
		{"\nfix(gate): subject после пустой строки\n", true},
		{"# This is a combination of 2 commits.\n# This is the 1st commit message:\n\nfeat(x): первый\n\n# This is the commit message #2:\n\nfix(x): второй\n", true},
		{"Revert \"fix(gate): pre-push проверяет пушимый коммит\"\n\nThis reverts commit abc.\n", true},
		{"fixup! fix(gate): pre-push проверяет пушимый коммит\n", true},
		{"squash! fix(gate): pre-push проверяет пушимый коммит\n", true},
		{"update stuff\n", false},
		{"meta(plan): план закрыт\n", false},
		{"Fix(gate): тип с заглавной\n", false},
		{"docs(README): scope с заглавной\n", false},
		{"fix(gate):без пробела\n", false},
		{"fix(gate):  subject с пробела\n", false},
		{"fix(gate): " + long + "я\n", false},
		{"feat(httpkit)!: NewServer с ошибкой\n\nтело без футера\n", false},
		{"feat(httpkit): NewServer с ошибкой" + footer, false},
		{"Merge branch 'main' into feature\n", false},
		{"# только комментарии редактора\n", false},
		{"\n", false},
	} {
		for _, locale := range []string{"C", "en_US.UTF-8"} {
			t.Run(locale+"/"+tc.msg, func(t *testing.T) {
				dir := t.TempDir() // вне репо: идущий в нём rebase на исход не влияет
				write(t, dir, map[string]string{"COMMIT_EDITMSG": tc.msg})
				out, err := run(t, dir, "", []string{"LC_ALL=" + locale}, hook, filepath.Join(dir, "COMMIT_EDITMSG"))
				if tc.ok != (err == nil) {
					t.Fatalf("commit-msg accepted = %v, want %v: %s", err == nil, tc.ok, out)
				}
			})
		}
	}
}

// Перенос давнего коммита не по форме после конфликта (cherry-pick --continue зовёт хук с его
// сообщением) проходит; новое сообщение не по форме — нет, и при идущем переносе тоже.
func TestCommitMsgHookOnCherryPick(t *testing.T) {
	hooks := filepath.Join(repoRoot(t), "scripts", "git-hooks")
	repo := t.TempDir()
	git := func(args ...string) (string, error) {
		t.Helper()
		args = append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "core.editor=true", "-c", "core.hooksPath=" + hooks}, args...)
		return run(t, repo, "", nil, "git", args...)
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := git(args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(file, content, msg string) {
		t.Helper()
		write(t, repo, map[string]string{file: content})
		must("add", file)
		// Давние коммиты сделаны до хука: здесь — мимо него.
		if out, err := run(t, repo, "", nil, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", msg); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	must("init", "-q", "-b", "main")
	commit("f", "base\n", "first commit")
	must("checkout", "-q", "-b", "old")
	commit("f", "old\n", "wip давний коммит не по форме")
	must("checkout", "-q", "main")
	commit("f", "new\n", "fix(x): правка того же файла")

	if out, err := git("cherry-pick", "old"); err == nil {
		t.Fatalf("cherry-pick must stop at the conflict:\n%s", out)
	}
	write(t, repo, map[string]string{"f": "merged\n"})
	must("add", "f")
	if out, err := git("commit", "-q", "-m", "update stuff"); err == nil {
		t.Fatalf("a new message that is not a Conventional Commit passes during cherry-pick:\n%s", out)
	}
	if out, err := git("cherry-pick", "--continue"); err != nil {
		t.Fatalf("cherry-pick --continue of an old commit with its own message is refused: %v\n%s", err, out)
	}

	write(t, repo, map[string]string{"g": "x\n"})
	must("add", "g")
	if out, err := git("commit", "-q", "-m", "update stuff"); err == nil {
		t.Fatalf("a new commit that is not a Conventional Commit passes:\n%s", out)
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
