package golibinfra_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Пакет без строки индекса потребитель и агент не найдут: корневой README и карта CLAUDE.md
// называют каждый кит, сабпакет testkit и пакет internal, README testkit — каждый свой сабпакет.
func TestDocsNameEveryPackage(t *testing.T) {
	readme, claude, testkitReadme := readDoc(t, "README.md"), readDoc(t, "CLAUDE.md"), readDoc(t, "testkit/README.md")
	named := func(doc, text, mention, pkg string) {
		t.Helper()
		if !strings.Contains(text, mention) {
			t.Errorf("%s: пакет %s не назван (%s)", doc, pkg, mention)
		}
	}
	for _, kit := range goDirs(t, ".") {
		if kit == "internal" || kit == "scripts" {
			continue
		}
		named("README.md § Пакеты", readme, "[`"+kit+"`]("+kit+"/)", kit)
		named("CLAUDE.md § Карта пакетов", claude, "`"+kit+"`", kit)
	}
	for _, sub := range goDirs(t, "testkit") {
		named("README.md, строка testkit", readme, "`"+sub+"`", "testkit/"+sub)
		named("CLAUDE.md § Карта пакетов", claude, "`"+sub+"`", "testkit/"+sub)
		named("testkit/README.md § Сабпакеты", testkitReadme, "[`"+sub+"`]("+sub+"/)", "testkit/"+sub)
	}
	for _, pkg := range goDirs(t, "internal") {
		named("README.md § Пакеты", readme, "`internal/"+pkg+"`", "internal/"+pkg)
		named("CLAUDE.md § Карта пакетов", claude, "`internal/"+pkg+"`", "internal/"+pkg)
	}
}

// Состав `make check` в доках — шаги GATE_STEPS: шага, которого нет в строке, читатель не ждёт.
func TestDocsListGateSteps(t *testing.T) {
	steps := regexp.MustCompile(`(?m)^override GATE_STEPS := (.+)$`).FindStringSubmatch(readDoc(t, "Makefile"))
	if steps == nil {
		t.Fatal("Makefile: нет GATE_STEPS")
	}
	names := strings.Fields(steps[1])
	for i, name := range names {
		if name == "vuln" { // шаг доки называют по инструменту
			names[i] = "govulncheck"
		}
	}
	line := regexp.MustCompile(`make check +# ` + regexp.QuoteMeta(strings.Join(names, " + ")) + `\n`)
	for _, doc := range []string{"README.md", "CLAUDE.md", "CONTRIBUTING.md"} {
		if !line.MatchString(readDoc(t, doc)) {
			t.Errorf("%s: строка `make check # …` не перечисляет шаги гейта: %s", doc, strings.Join(names, " + "))
		}
	}
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// goDirs — подкаталоги dir с Go-кодом на любой глубине (у hubkit код только в сабпакетах), без
// скрытых и testdata.
func goDirs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "testdata" {
			continue
		}
		hasGo := false
		err := filepath.WalkDir(filepath.Join(dir, e.Name()), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				hasGo = true
				return filepath.SkipAll
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if hasGo {
			out = append(out, e.Name())
		}
	}
	return out
}
