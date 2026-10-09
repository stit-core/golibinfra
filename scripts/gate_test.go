package scripts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// shellForgeries — окружение оболочки, которым вызывающий подменил бы bash-скрипт: файл
// автозапуска (BASH_ENV), функции с именами его команд (export -f) и опция «читать, но не
// исполнять» (SHELLOPTS). Скрипты гейта и хуки стартуют `bash -p` и этого не читают.
func shellForgeries(t *testing.T) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, map[string]string{"exit0.sh": "exit 0\n"})
	return map[string][]string{
		"BASH_ENV":         {"BASH_ENV=" + filepath.Join(dir, "exit0.sh")},
		"function set":     {"BASH_FUNC_set%%=() { exit 0; }"},
		"function env":     {"BASH_FUNC_env%%=() { :; }"},
		"function read":    {"BASH_FUNC_read%%=() { return 1; }"},
		"SHELLOPTS=noexec": {"SHELLOPTS=noexec"},
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
	// Регрессия RGATE-01 (окружение оболочки): сообщение не по форме не проходит, чем бы ни было
	// окружение коммитящего.
	for name, env := range shellForgeries(t) {
		dir := t.TempDir()
		write(t, dir, map[string]string{"COMMIT_EDITMSG": "update stuff\n"})
		if out, err := run(t, dir, "", env, hook, filepath.Join(dir, "COMMIT_EDITMSG")); err == nil {
			t.Errorf("%s in the committer's environment switches the hook off: a message that is not a Conventional Commit passes:\n%s", name, out)
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
	const gate = "check:\n\t@echo \"toy gate: $$(cat verdict) goals=$(MAKECMDGOALS) make=$(notdir $(MAKE)) shell=[$${BASH_ENV-}$${SHELLOPTS-}$${BASHOPTS-}]\"; test \"$$(cat verdict)\" = green\n"
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

	// Регрессия RGATE-01: переменные make из окружения пушащего до гейта не доходят — ни флаги,
	// глушащие ошибки шагов, ни MAKECMDGOALS и MAKE, которыми выключается самозащита make check.
	forge := filepath.Join(tmp, "forge.mk")
	write(t, tmp, map[string]string{"forge.mk": "check::\n\t@echo forged\n"})
	out, err = run(t, repo, "refs/heads/main "+red+" refs/heads/main "+zero+"\n", []string{
		"TMPDIR=" + tmp, "MAKEFLAGS=i", "MFLAGS=-i", "GNUMAKEFLAGS=-i", "MAKEFILES=" + forge, "MAKECMDGOALS=x", "MAKE=true", "MAKELEVEL=3",
	}, hook)
	if err == nil || !strings.Contains(out, "toy gate: red goals=check make=make") || strings.Contains(out, "forged") {
		t.Fatalf("make variables of the pusher's environment reach the gate (err %v):\n%s", err, out)
	}

	// Регрессия RGATE-01 (окружение оболочки): файл автозапуска, экспортированные функции и опции
	// bash из окружения пушащего хук не выключают и до гейта не доходят.
	for name, env := range shellForgeries(t) {
		out, err := run(t, repo, "refs/heads/main "+red+" refs/heads/main "+zero+"\n", append([]string{"TMPDIR=" + tmp}, env...), hook)
		if err == nil || !strings.Contains(out, "toy gate: red goals=check make=make shell=[]") {
			t.Errorf("%s in the pusher's environment: a red commit passes or the variable reaches the gate (err %v):\n%s", name, err, out)
		}
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

// Флаги make, переменные вызывающего и лишний makefile не подделывают вердикт гейта. Настоящий гейт
// здесь падает на первом шаге с golangci-lint — в PATH не пиненная версия, — поэтому проба не гоняет
// его целиком. Формы F и T — формы запуска повторного аудита (RGATE-01): отказы самозащиты
// включались по MAKECMDGOALS, а её задаёт вызывающий. Makefile под подделку (переприсваивает
// MAKEFILE_LIST) обходит любую сверку внутри make: это правка гейта.
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
	// Предохранитель: go вложенного make отвечает только на `go env` (по нему Makefile узнаёт
	// GOFLAGS). Форма, прошедшая самозащиту, с флагом -i дошла бы до test-full настоящего гейта,
	// а он снова запустил бы этот тест; без go шаги падают сразу, и подделка видна за секунды.
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	write(t, bin, map[string]string{"go": "#!/bin/sh\n[ \"$1\" = env ] && exec \"" + realGo + "\" \"$@\"\necho \"go $*: the forge probe has no toolchain\" >&2\nexit 1\n"})
	if err := os.Chmod(filepath.Join(bin, "go"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, fakes, map[string]string{
		"colon.mk":  "check gate-clean:\n\t@echo \"" + gateVerdict + "\"\n",
		"double.mk": "check gate-clean::\n\t@echo \"" + gateVerdict + "\"\n",
		"shell.mk":  "override SHELL := /bin/true\n",
		"goals.mk":  "MAKECMDGOALS := x\n",
	})
	colon, double, shell := filepath.Join(fakes, "colon.mk"), filepath.Join(fakes, "double.mk"), filepath.Join(fakes, "shell.mk")
	goals := filepath.Join(fakes, "goals.mk")
	steps := []string{"hooks-check", "fmt-check", "vet", "lint", "test-full", "build", "tidy-check", "vuln", "envdoc-check", "plan-check"}
	oldSteps := make([]string, 0, 2*len(steps))
	for _, step := range steps {
		oldSteps = append(oldSteps, "-o", step)
	}
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

		{name: "F02 -o vet", args: []string{"-o", "vet", "check"}, gate: true},
		{name: "F03 --old-file", args: []string{"--old-file=vet", "check"}, gate: true},
		{name: "F04 --assume-old", args: []string{"--assume-old=vet", "check"}, gate: true},
		{name: "F05 -o for every step", args: append(oldSteps, "check"), gate: true},
		{name: "F06 MAKEFLAGS=-o vet in env", args: []string{"check"}, env: []string{"MAKEFLAGS=-o vet"}, gate: true},
		{name: "F07 -i with MAKECMDGOALS on the command line", args: []string{"-i", "check", "MAKECMDGOALS=x"}},
		{name: "F08 -i with MAKECMDGOALS in env", args: []string{"-i", "check"}, env: []string{"MAKECMDGOALS=x"}},
		{name: "F09 MAKE and MAKECMDGOALS in env", args: []string{"check"}, env: []string{"MAKE=true", "MAKECMDGOALS=x"}},
		{name: "F10 MAKEFLAGS=i and MAKECMDGOALS in env", args: []string{"check"}, env: []string{"MAKECMDGOALS=x", "MAKEFLAGS=i"}},
		{name: "F11 MAKEFLAGS=i on the command line", args: []string{"check", "MAKEFLAGS=i"}},
		{name: "F12 --ignore-errors", args: []string{"--ignore-errors", "check"}},
		{name: "F13 --keep-going", args: []string{"--keep-going", "check"}},
		{name: "F14 --dry-run", args: []string{"--dry-run", "check"}},
		{name: "F15 --touch", args: []string{"--touch", "check"}},
		{name: "F16 --question", args: []string{"--question", "check"}},
		{name: "F17 CURDIR", args: []string{"check", "CURDIR=/tmp"}, gate: true},
		{name: "F18 -i, second makefile and MAKEFILE_LIST", args: []string{"-i", "-f", "Makefile", "-f", double, "check", "MAKEFILE_LIST=Makefile"}},
		{name: "F19 -k, second makefile, MAKEFILE_LIST and MAKECMDGOALS", args: []string{"-k", "-f", "Makefile", "-f", double, "check", "MAKEFILE_LIST=Makefile", "MAKECMDGOALS=x"}},
		{name: "F20 override variables on the command line", args: []string{"check", "gate_bad=", "gate_refuse=", "gate_forged=", "GATE=true", "GATE_STEPS="}, gate: true},
		{name: "F21 -e with GATE in env", args: []string{"-e", "check"}, env: []string{"GATE=true", "GATE_STEPS="}, gate: true},
		{name: "F22 two goals", args: []string{"check", "vet"}, gate: true},
		{name: "F23 two goals and -o", args: []string{"vet", "check", "-o", "vet"}, gate: true},
		{name: "F24 -j8", args: []string{"-j8", "check"}, gate: true},
		{name: "F25 -s", args: []string{"-s", "check"}, gate: true},
		{name: "F26 -B", args: []string{"-B", "check"}, gate: true},
		{name: "F27 -W", args: []string{"check", "-W", "vet"}, gate: true},
		{name: "F28 --eval of GATE_STEPS", args: []string{"check", "--eval=override GATE_STEPS := hooks-check"}},
		{name: "F29 GO, PKGS and A", args: []string{"check", "GO=true", "PKGS=./flagkit/...", "A=--summary"}, gate: true},
		{name: "F30 GOFLAGS in env", args: []string{"check"}, env: []string{"GOFLAGS=-vet=off"}},
		{name: "T02 GOFLAGS on the command line", args: []string{"check", "GOFLAGS=-run=^$"}},
		{name: "T03 -o test-full", args: []string{"-o", "test-full", "check"}, gate: true},
		{name: "T04 GOFLAGS and MAKECMDGOALS in env", args: []string{"check"}, env: []string{"GOFLAGS=-run=^$", "MAKECMDGOALS=x"}},

		{name: "MAKECMDGOALS from a makefile read before", args: []string{"-i", "-f", goals, "-f", "Makefile", "check"}},
		{name: "MAKEFILE_LIST alone on the command line", args: []string{"-f", "Makefile", "-f", double, "check", "MAKEFILE_LIST=Makefile"}},
		{name: "-i behind a goal alias from --eval", args: []string{"-i", "--eval", "x: check", "x"}},
		{name: "-k behind a goal alias from a second makefile", args: []string{"-k", "-f", "Makefile", "-f", double, "--eval", "x: check", "x"}},
		{name: "gate-clean: -i with MAKECMDGOALS", args: []string{"-i", "gate-clean", "MAKECMDGOALS=x"}},

		{name: "BASH_ENV in env", args: []string{"check"}, env: shellForgeries(t)["BASH_ENV"], gate: true},
		{name: "SHELLOPTS=noexec in env", args: []string{"check"}, env: shellForgeries(t)["SHELLOPTS=noexec"], gate: true},
		{name: "exported function set in env", args: []string{"check"}, env: shellForgeries(t)["function set"], gate: true},
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
			if !tc.gate && strings.Contains(out, "→ hooks-check") {
				t.Fatalf("make %v must refuse to start the gate:\n%s", tc.args, out)
			}
			// До fmt-check гейт доходит с активными хуками (make hooks); дальше его не пускает версия тула.
			if strings.Contains(out, "→ fmt-check") && !strings.Contains(out, "пиненную версию") {
				t.Fatalf("make %v must stop at the tool that is not the pinned version:\n%s", tc.args, out)
			}
			if strings.Contains(out, "→ vet") {
				t.Fatalf("make %v went past the failed step:\n%s", tc.args, out)
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

// Регрессия RGATE-03: //nolint с именем линтера, которого нет в enable, — отказ lint до прогона.
// Такая директива ничего не подавляет, и nolintlint её не видит: включат линтер — его замечание
// заглушено заранее.
func TestLintRefusesNolintOfDisabledLinter(t *testing.T) {
	root := repoRoot(t)
	// Директива собрана из частей: проверка читает и этот файл.
	files := map[string]string{"a.go": "package a\n\nfunc f() (*int, error) { return nil, nil } //nol" + "int:gosec,nilnil // не нужно\n"}
	for _, name := range []string{".golangci.yml", "scripts/linters.txt"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	dir := t.TempDir()
	write(t, dir, files)
	out, err := run(t, dir, "", nil, "bash", filepath.Join(root, "scripts", "lint.sh"))
	if err == nil || !strings.Contains(out, "//nolint называет линтер вне enable .golangci.yml: nilnil") || !strings.Contains(out, "a.go:3:") {
		t.Fatalf("lint must refuse a //nolint of a linter that is not enabled (err %v):\n%s", err, out)
	}
}

// Регрессия RGATE-01 (окружение оболочки): шаги-скрипты гейта — lint и plan-check — на красном
// дереве остаются красными при любом окружении оболочки вызывающего, и там, где sh — это bash
// (SHELL=/bin/bash: рецепт читал бы BASH_ENV и SHELLOPTS сам). Дерево игрушечное: настоящие
// Makefile и скрипты, конфиг lint со снятым линтером, бэклога планов нет.
func TestGateScriptStepsIgnoreShellEnvironment(t *testing.T) {
	root := repoRoot(t)
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	const dropped = "\n    - errcheck\n"
	config := read(".golangci.yml")
	if strings.Count(config, dropped) != 1 {
		t.Fatalf("anchor %q: want one match in .golangci.yml", dropped)
	}
	dir := t.TempDir()
	files := map[string]string{".golangci.yml": strings.Replace(config, dropped, "\n", 1)}
	scripts := []string{"scripts/lint.sh", "scripts/validate-plan.sh"}
	for _, name := range append([]string{"Makefile", "scripts/linters.txt"}, scripts...) {
		files[name] = read(name)
	}
	write(t, dir, files)
	for _, name := range scripts {
		if err := os.Chmod(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	forgeries := shellForgeries(t)
	forgeries["clean environment"] = nil
	for step, refusal := range map[string]string{"lint": "снят: errcheck", "plan-check": "бэклог обязателен"} {
		for name, env := range forgeries {
			for _, shell := range []string{"", "SHELL=/bin/bash"} {
				args := []string{step}
				if shell != "" {
					args = append(args, shell)
				}
				out, err := run(t, dir, "", env, "make", args...)
				if err == nil || !strings.Contains(out, refusal) {
					t.Errorf("make %v with %s: the step must stay red with %q (err %v):\n%s", args, name, refusal, err, out)
				}
			}
		}
	}
}

// Скрипты гейта и хуки git стартуют `bash -p` своей строкой интерпретатора: так их запускают git
// и Makefile, и новый скрипт без неё читал бы окружение оболочки вызывающего.
func TestGateScriptsStartPrivileged(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, pattern := range []string{"scripts/*.sh", "scripts/git-hooks/*"} {
		found, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, found...)
	}
	if len(files) < 5 {
		t.Fatalf("scripts of the gate: %v — the list is cut", files)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if first, _, _ := strings.Cut(string(data), "\n"); first != "#!/bin/bash -p" {
			t.Errorf("%s starts with %q, want #!/bin/bash -p", file, first)
		}
	}
}

// Регрессия RGATE-05: проводку guard держит тест. TestGuardBash гоняет сам скрипт, а снятая
// секция hooks или deny в .claude/settings.json оставляли след только в диффе.
func TestSettingsWireTheGuard(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(settings.Permissions.Deny, "Bash(*--no-veri*)") {
		t.Errorf("permissions.deny lost the --no-verify rule: %v", settings.Permissions.Deny)
	}
	wired := false
	for _, entry := range settings.Hooks.PreToolUse {
		for _, h := range entry.Hooks {
			wired = wired || entry.Matcher == "Bash" && h.Type == "command" && strings.Contains(h.Command, ".claude/hooks/guard-bash.sh")
		}
	}
	if !wired {
		t.Errorf("hooks.PreToolUse does not call guard-bash.sh for Bash: %s", data)
	}
}
