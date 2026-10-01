#!/usr/bin/env bash
# Гоняет `make check` в отдельном worktree от коммита (по умолчанию HEAD): только то, что
# реально в git. Ловит файлы, которые есть локально, но не закоммичены (gitignored embed и т.п.).
# Кеш golangci-lint у свежего worktree свой и холодный (Makefile кладёт его в checkout).
#
#   scripts/gate-clean.sh [<commit>]
set -euo pipefail

root=$(git rev-parse --show-toplevel)
commit=$(git rev-parse --verify "${1:-HEAD}^{commit}")
# Хук git (pre-push) экспортирует GIT_DIR и соседей: в другом worktree они указали бы не туда.
# shellcheck disable=SC2046 # имена переменных — отдельными словами
unset $(git rev-parse --local-env-vars)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/golibinfra-gate.XXXXXX")
cleanup() {
	git -C "$root" worktree remove --force "$tmp" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT

git -C "$root" worktree add --detach --quiet "$tmp" "$commit"
echo "gate-clean: worktree $tmp @ $(git -C "$root" rev-parse --short "$commit")"
# Флаги и makefile'ы вызывающего make (MAKEFLAGS=i, -n…) до гейта не доходят.
env -u MAKEFLAGS -u MFLAGS -u GNUMAKEFLAGS -u MAKEFILES make -C "$tmp" check
