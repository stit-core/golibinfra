// Package archkit — машинные правила раскладки сервиса на golibinfra: каталоги internal/, импорты
// между доменами и слоями, корень домена без транспорта и I/O, чистые слои, часы, порты между
// доменами и свои таблицы. Зовётся из теста потребителя (Check); сигналов не эмитит.
package archkit

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Option настраивает Check под сервис потребителя; nil ничего не меняет.
type Option func(*options)

type options struct {
	rootLibs     []string
	purePackages []string
	unchecked    []string
	internalDirs []string
	known        map[string]string
}

// WithRootLibs называет внешние пакеты (путь импорта целиком), которые видит корень домена сверх
// stdlib без I/O: порты и значения китов, DI. Подпакет названного пакета под него не попадает.
func WithRootLibs(pkgs ...string) Option {
	return func(o *options) { o.rootLibs = append(o.rootLibs, pkgs...) }
}

// WithPurePackages называет пакеты модуля (путь от его корня), которые видят чистые слои домена.
// Корень домена проверяет их импорты, как у любого пакета pkg/ и internal/platform/.
func WithPurePackages(pkgs ...string) Option {
	return func(o *options) { o.purePackages = append(o.purePackages, pkgs...) }
}

// WithUncheckedPackages называет пакеты pkg/ и internal/platform/ (путь от корня модуля), в импорты
// которых правило корня домена не заглядывает: за их чистоту отвечает потребитель, машина её не
// держит. Запись о пакете, который чист и без неё, и запись мимо этих каталогов — отказ.
func WithUncheckedPackages(pkgs ...string) Option {
	return func(o *options) { o.unchecked = append(o.unchecked, pkgs...) }
}

// WithInternalDirs называет каталоги internal/ потребителя (путь от корня модуля) сверх
// internal/domain и internal/platform; домены, platform и pkg их не импортируют.
func WithInternalDirs(dirs ...string) Option {
	return func(o *options) { o.internalDirs = append(o.internalDirs, dirs...) }
}

// WithKnownViolations называет известные нарушения: текст отказа до причины → ID находки
// (ADP-02, PROJ-1234). Запись без такого ID и запись, которой не отвечает нарушение, — отказ.
func WithKnownViolations(known map[string]string) Option {
	return func(o *options) {
		if o.known == nil {
			o.known = map[string]string{}
		}
		maps.Copy(o.known, known)
	}
}

// Check проверяет раскладку модуля, из каталога которого запущен тест: корень и module path — из
// ближайшего go.mod вверх от рабочего каталога. Нарушение правила — t.Errorf, по строке на
// нарушение; дерево, которое правила не видят (нет go.mod, доменов, таблиц при запросах), — t.Fatalf.
func Check(t testing.TB, opts ...Option) {
	t.Helper()
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	var failures []string
	root, err := moduleRoot()
	if err == nil {
		failures, err = inspect(os.DirFS(root), o)
	}
	if err != nil {
		t.Fatalf("archkit: %v", err)
		return
	}
	for _, f := range failures {
		t.Errorf("archkit: %s", f)
	}
}

// moduleRoot — каталог ближайшего go.mod от рабочего каталога вверх.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod in the working directory or above it")
		}
		dir = parent
	}
}

// rules — правила в порядке отчёта: нарушение, названное раньше, позже не повторяется.
var rules = []func(*tree){
	(*tree).internalLayout,
	(*tree).importBoundaries,
	(*tree).pureLayers,
	(*tree).domainRoot,
	(*tree).wallClock,
	(*tree).identityClosures,
	(*tree).ownTables,
}

// inspect — строки отказа по модулю в fsys: нарушения правил вне известных и записи известных
// нарушений без ID находки или без нарушения.
func inspect(fsys fs.FS, o options) ([]string, error) {
	for _, dir := range o.internalDirs {
		// Каталог выше или мимо internal/<имя> («internal») выключил бы правило каталогов целиком.
		if name, ok := strings.CutPrefix(dir, "internal/"); !ok || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("WithInternalDirs(%q): a directory of internal/ is named from the module root, such as internal/testkit", dir)
		}
	}
	for _, pkg := range o.unchecked {
		if !lowLayer(pkg) {
			return nil, fmt.Errorf("WithUncheckedPackages(%q): a package of pkg/ or internal/platform/ is named from the module root, such as internal/platform/identity", pkg)
		}
	}
	t, err := load(fsys, o)
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		rule(t)
	}
	var failures []string
	for _, v := range t.found {
		if _, known := o.known[v.what]; !known {
			failures = append(failures, v.what+": "+v.why)
		}
	}
	for _, pkg := range o.unchecked {
		if t.packageIOChain(pkg) == nil {
			failures = append(failures, fmt.Sprintf("unchecked package %q sees nothing the domain root does not: remove it from WithUncheckedPackages", pkg))
		}
	}
	for _, what := range slices.Sorted(maps.Keys(o.known)) {
		switch id := o.known[what]; {
		case !findingID.MatchString(id):
			failures = append(failures, fmt.Sprintf("known violation %q needs a finding ID such as ADP-02, got %q", what, id))
		case !t.seen[what]:
			failures = append(failures, fmt.Sprintf("known violation %q (%s) matches no violation: remove it", what, id))
		}
	}
	return failures, nil
}

// findingID — ID находки аудита или задачи трекера: буквенный префикс, дефис, номер.
var findingID = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)
