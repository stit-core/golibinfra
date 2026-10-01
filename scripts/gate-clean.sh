#!/usr/bin/env bash
# Гоняет `make check` в отдельном worktree от HEAD: только то, что реально в git.
# Ловит файлы, которые есть локально, но не закоммичены (gitignored embed и т.п.).
# Кеш golangci-lint у свежего worktree свой и холодный (Makefile кладёт его в checkout).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/golibinfra-gate.XXXXXX")
cleanup() {
	git -C "$root" worktree remove --force "$tmp" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

git -C "$root" worktree add --detach --quiet "$tmp" HEAD
echo "gate-clean: worktree $tmp @ $(git -C "$root" rev-parse --short HEAD)"
# Флаги и makefile'ы вызывающего make (MAKEFLAGS=i, -n…) до гейта не доходят.
env -u MAKEFLAGS -u MFLAGS -u GNUMAKEFLAGS -u MAKEFILES make -C "$tmp" check
