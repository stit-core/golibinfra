package golibinfra_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Пакет без строки индекса потребитель и агент не найдут: кит — строка таблицы корневого README
// и имя в карте CLAUDE.md, пакет internal назван в обоих индексах, сабпакет testkit — в их
// записях testkit и строкой таблицы README testkit. Упоминание вне индекса не в счёт: имя,
// названное в другом разделе, строку индекса не заменяет.
func TestDocsNameEveryPackage(t *testing.T) {
	readme := docSection(t, "README.md", "## Пакеты")
	claude := docSection(t, "CLAUDE.md", "## Карта пакетов")
	testkitReadme := docSection(t, "testkit/README.md", "## Сабпакеты")
	readmeTestkit := indexEntry(readme, "[`testkit`](testkit/)", "\n")
	claudeTestkit := indexEntry(claude, "`testkit`", "\n- ")
	named := func(doc, text, mention, pkg string) {
		t.Helper()
		if !strings.Contains(text, mention) {
			t.Errorf("%s: пакет %s не назван (%s)", doc, pkg, strings.TrimSpace(mention))
		}
	}
	for _, kit := range goDirs(t, ".") {
		if kit == "internal" || kit == "scripts" {
			continue
		}
		named("README.md § Пакеты", readme, "\n| [`"+kit+"`]("+kit+"/) |", kit)
		named("CLAUDE.md § Карта пакетов", claude, "`"+kit+"`", kit)
	}
	for _, sub := range goDirs(t, "testkit") {
		named("README.md § Пакеты, строка testkit", readmeTestkit, "`"+sub+"`", "testkit/"+sub)
		named("CLAUDE.md § Карта пакетов, запись testkit", claudeTestkit, "`"+sub+"`", "testkit/"+sub)
		named("testkit/README.md § Сабпакеты", testkitReadme, "\n| [`"+sub+"`]("+sub+"/) |", "testkit/"+sub)
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

// docSection — текст раздела доки: от строки заголовка до следующего заголовка второго уровня.
func docSection(t *testing.T, path, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(readDoc(t, path), "\n"+heading+"\n")
	if !ok {
		t.Fatalf("%s: нет раздела %q", path, heading)
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return section
}

// indexEntry — запись индекса с упоминанием mention: от разделителя записей sep перед ним до
// следующего. Без упоминания записи нет — пустая строка.
func indexEntry(index, mention, sep string) string {
	before, after, ok := strings.Cut(index, mention)
	if !ok {
		return ""
	}
	if start := strings.LastIndex(before, sep); start >= 0 {
		before = before[start:]
	}
	after, _, _ = strings.Cut(after, sep)
	return before + mention + after
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
