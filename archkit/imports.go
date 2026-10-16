package archkit

import (
	"path"
	"slices"
	"strings"
)

const (
	whyLayout = "internal/ holds domain, platform and the WithInternalDirs directories only — business code goes to internal/domain/<d>, code shared by domains to pkg/"
	whyPkg    = "pkg/ holds the WithPkgPackages packages only — a new package is named there, a generic primitive goes to golibinfra"

	whyMocks       = "mocks are for tests only"
	whyLowLayer    = "platform and pkg lie below the domains: they import only platform and pkg"
	whyBridge      = "besides domains, a domain imports only platform and pkg: any other package would bridge domains around the ports"
	whyOutbox      = "a domain outbox is mounted by its own module/ and migrated by cmd/migrate only"
	whySqlc        = "sqlc types stay inside the domain's infra/postgres"
	whyCrossDomain = "another domain is reached through a port and an adapter in module/"
	whyModuleRoot  = "module/ reaches another domain through its root package only"
	whyRootPorts   = "the domain root depends on ports, not on its own subpackages"
	whyDelivery    = "delivery calls the usecase, not infra"

	whyRoot = "the domain root sees stdlib without I/O, the WithRootLibs packages and those packages of pkg/ and internal/platform/ that see nothing else — transport, drivers and I/O go through ports"
	whyPure = "pure layers (entity.go, policy.go, dto.go, errors.go, events.go) see side-effect-free stdlib and the WithPurePackages packages only"
)

// internalLayout: код internal/ вне своих каталогов не видит ни одно правило слоёв — бизнес-код
// в нём обошёл бы границы доменов.
func (t *tree) internalLayout() {
	roots := append([]string{domainDir, platformDir}, t.opts.internalDirs...)
	for _, dir := range t.codeDirs {
		if !slices.ContainsFunc(roots, func(root string) bool { return under(dir, root) }) {
			t.add(dir, whyLayout)
		}
	}
}

// pkgLayout: пакет pkg/ — запись настроек потребителя, а не только файл в ревью кода: generic-примитив,
// положенный в pkg/, виден в диффе настроек.
func (t *tree) pkgLayout() {
	for _, pkg := range t.pkgPackages() {
		if !slices.Contains(t.opts.pkgPackages, pkg) {
			t.add(pkg, whyPkg)
		}
	}
}

// importBoundaries держит границы не-тестового кода модуля: домены, их слои, platform и pkg.
func (t *tree) importBoundaries() {
	for _, f := range t.files {
		if f.test {
			continue
		}
		for _, imp := range f.imports {
			target, ok := t.inModule(imp)
			if !ok {
				continue
			}
			if why := importViolation(f.pkg, target); why != "" {
				t.add(f.path+" imports "+target, why)
			}
		}
	}
}

// importViolation — почему пакет from не вправе импортировать пакет модуля target (пути от корня
// модуля); "" — вправе.
func importViolation(from, target string) string {
	fromDomain, fromSub, inDomain := splitDomain(from)
	toDomain, toSub, toDomainPkg := splitDomain(target)
	switch {
	case path.Base(target) == "mocks":
		return whyMocks
	case lowLayer(from) && !lowLayer(target):
		return whyLowLayer
	case inDomain && !toDomainPkg && !lowLayer(target):
		return whyBridge
	case !toDomainPkg:
		return ""
	case toSub == "infra/outbox" && from != domainDir+"/"+toDomain+"/module" && !under(from, "cmd/migrate"):
		return whyOutbox
	case toSub == "infra/postgres/db" && from != domainDir+"/"+toDomain+"/infra/postgres":
		return whySqlc
	case !inDomain:
		return ""
	case fromDomain != toDomain && fromSub != "module":
		return whyCrossDomain
	case fromDomain != toDomain && toSub != "":
		return whyModuleRoot
	case fromSub == "" && toSub != "":
		return whyRootPorts
	case under(fromSub, "delivery") && under(toSub, "infra"):
		return whyDelivery
	}
	return ""
}

// lowLayer — platform и pkg: на них опираются домены, сами они доменов и прочего не знают.
func lowLayer(pkg string) bool {
	return under(pkg, platformDir) || under(pkg, pkgDir)
}

// pureLayerFiles — файлы корня домена без I/O и зависимостей от китов.
var pureLayerFiles = []string{"entity.go", "policy.go", "dto.go", "errors.go", "events.go"}

// pureLayers: чистые слои видят stdlib без побочных эффектов и потоков и пакеты модуля, которые
// назвал потребитель.
func (t *tree) pureLayers() {
	for _, f := range t.domainRootFiles() {
		if !slices.Contains(pureLayerFiles, path.Base(f.path)) {
			continue
		}
		for _, imp := range f.imports {
			if !t.pureAllowed(imp) {
				t.add(f.path+" imports "+t.name(imp), whyPure)
			}
		}
	}
}

func (t *tree) pureAllowed(imp string) bool {
	if rel, ok := t.inModule(imp); ok {
		return slices.Contains(t.opts.purePackages, rel)
	}
	return stdlib(imp) && !ioStdlib(imp) && !under(imp, "io") && imp != "bufio"
}

// domainRoot: корень домена не знает транспорта, драйверов и I/O — ни напрямую, ни через пакет
// pkg/ или internal/platform/.
func (t *tree) domainRoot() {
	for _, f := range t.domainRootFiles() {
		for _, imp := range f.imports {
			chain := t.ioChain(imp)
			if chain == nil {
				continue
			}
			why := whyRoot
			if len(chain) > 1 {
				why += " (" + strings.Join(chain, " → ") + ")"
			}
			t.add(f.path+" imports "+chain[0], why)
		}
	}
}

// ioChain — путь от импорта корня домена до пакета, который корню не виден; nil — импорт чист:
// stdlib без I/O, пакет WithRootLibs, а пакет pkg/ и internal/platform/ — когда чист каждый импорт
// его не-тестовых файлов или потребитель назвал его в WithUncheckedPackages. Прочие пакеты модуля
// держит importBoundaries.
func (t *tree) ioChain(imp string) []string {
	rel, inModule := t.inModule(imp)
	switch {
	case inModule && (!lowLayer(rel) || slices.Contains(t.opts.unchecked, rel)):
		return nil
	case inModule:
		return t.packageIOChain(rel)
	case slices.Contains(t.opts.rootLibs, imp), stdlib(imp) && !ioStdlib(imp):
		return nil
	}
	return []string{imp}
}

func (t *tree) packageIOChain(pkg string) []string {
	if chain, done := t.ioChains[pkg]; done {
		return chain
	}
	// Цикл импортов не компилируется; запись до обхода — от бесконечной рекурсии на таком дереве.
	t.ioChains[pkg] = nil
	for _, imp := range t.pkgImports[pkg] {
		if chain := t.ioChain(imp); chain != nil {
			t.ioChains[pkg] = append([]string{pkg}, chain...)
			break
		}
	}
	return t.ioChains[pkg]
}

// importedByDomainRoots — пакеты pkg/ и internal/platform/, которые берёт корень какого-либо домена:
// сам или по импортам не-тестовых файлов таких же пакетов. Только до них доходит правило корня.
func (t *tree) importedByDomainRoots() map[string]bool {
	reached := map[string]bool{}
	var walk func(imports []string)
	walk = func(imports []string) {
		for _, imp := range imports {
			if rel, ok := t.inModule(imp); ok && lowLayer(rel) && !reached[rel] {
				reached[rel] = true
				walk(t.pkgImports[rel])
			}
		}
	}
	for _, f := range t.domainRootFiles() {
		walk(f.imports)
	}
	return reached
}

func (t *tree) domainRootFiles() []goFile {
	var root []goFile
	for _, f := range t.files {
		if f.domainCode() && f.sub == "" {
			root = append(root, f)
		}
	}
	return root
}

// ioPackages — stdlib, которая ходит в ОС, сеть или БД (пакет и всё под ним); парсеры из net — не I/O.
var (
	ioPackages = []string{"database/sql", "net", "os", "syscall"}
	netParsers = []string{"net/mail", "net/netip", "net/url"}
)

func ioStdlib(imp string) bool {
	return !slices.Contains(netParsers, imp) && slices.ContainsFunc(ioPackages, func(dir string) bool { return under(imp, dir) })
}

// stdlib — пакет стандартной библиотеки: первый элемент его пути без точки.
func stdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}
