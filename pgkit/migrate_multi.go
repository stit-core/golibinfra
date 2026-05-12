package pgkit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// Source — один источник миграций (kit'овый embed.FS или app-овый os.DirFS).
//
// Composer склеивает все Sources в один tmp-dir с глобальной перенумерацией
// 1..N в порядке элементов в `[]Source`, затем в порядке версии внутри
// каждого источника.
//
// Invariant для прод-БД: порядок Sources в caller-коде append-only. Новый
// источник миграций добавляется в КОНЕЦ списка, чтобы не сдвигать существующие
// version'ы в schema_migrations. Для локального dev (drop+recreate) это не
// критично.
type Source struct {
	Name   string // диагностическая метка, попадает в ошибки composer'а
	FS     fs.FS  // typically embed.FS из kit'а или os.DirFS(appPath)
	Prefix string // subdir внутри FS, обычно "migrations"; для os.DirFS — "."
}

type MultiSourceConfig struct {
	DSN     string
	Sources []Source
}

var migFileRe = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)

type migPair struct {
	version int
	name    string
	upData  []byte
	dnData  []byte
	origin  string
}

func collectFromSource(src Source) ([]migPair, error) {
	entries, err := fs.ReadDir(src.FS, src.Prefix)
	if err != nil {
		return nil, fmt.Errorf("pgkit: read source %q: %w", src.Name, err)
	}
	byKey := map[string]*migPair{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, _ := strconv.Atoi(m[1])
		name := m[2]
		kind := m[3]
		p := filepath.ToSlash(filepath.Join(src.Prefix, e.Name()))
		data, err := fs.ReadFile(src.FS, p)
		if err != nil {
			return nil, fmt.Errorf("pgkit: read %s/%s: %w", src.Name, e.Name(), err)
		}
		key := fmt.Sprintf("%d_%s", v, name)
		pair, ok := byKey[key]
		if !ok {
			pair = &migPair{version: v, name: name, origin: src.Name}
			byKey[key] = pair
		}
		if kind == "up" {
			pair.upData = data
		} else {
			pair.dnData = data
		}
	}
	out := make([]migPair, 0, len(byKey))
	for _, p := range byKey {
		if p.upData == nil {
			return nil, fmt.Errorf("pgkit: source %q migration %d_%s missing .up.sql", src.Name, p.version, p.name)
		}
		if p.dnData == nil {
			return nil, fmt.Errorf("pgkit: source %q migration %d_%s missing .down.sql", src.Name, p.version, p.name)
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func composeMigrations(cfg MultiSourceConfig) (string, error) {
	var all []migPair
	for _, s := range cfg.Sources {
		ps, err := collectFromSource(s)
		if err != nil {
			return "", err
		}
		all = append(all, ps...)
	}
	if len(all) == 0 {
		return "", errors.New("pgkit: no migrations found in any source")
	}

	tmp, err := os.MkdirTemp("", "pgkit-multi-")
	if err != nil {
		return "", fmt.Errorf("pgkit: mkdir tmp: %w", err)
	}

	for i, p := range all {
		g := i + 1
		upName := fmt.Sprintf("%04d_%s.up.sql", g, p.name)
		dnName := fmt.Sprintf("%04d_%s.down.sql", g, p.name)
		if err := os.WriteFile(filepath.Join(tmp, upName), p.upData, 0o644); err != nil {
			_ = os.RemoveAll(tmp)
			return "", fmt.Errorf("pgkit: write %s: %w", upName, err)
		}
		if err := os.WriteFile(filepath.Join(tmp, dnName), p.dnData, 0o644); err != nil {
			_ = os.RemoveAll(tmp)
			return "", fmt.Errorf("pgkit: write %s: %w", dnName, err)
		}
	}

	return tmp, nil
}

func MigrateUpMulti(cfg MultiSourceConfig) error {
	tmp, err := composeMigrations(cfg)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	return MigrateUp(MigrateConfig{DSN: cfg.DSN, Path: tmp})
}

func MigrateDownMulti(cfg MultiSourceConfig) error {
	tmp, err := composeMigrations(cfg)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	return MigrateDown(MigrateConfig{DSN: cfg.DSN, Path: tmp})
}

func MigrateVersionMulti(cfg MultiSourceConfig) (uint, bool, error) {
	tmp, err := composeMigrations(cfg)
	if err != nil {
		return 0, false, err
	}
	defer os.RemoveAll(tmp)
	return MigrateVersion(MigrateConfig{DSN: cfg.DSN, Path: tmp})
}
