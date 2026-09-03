#!/usr/bin/env bash
# Гоняет `make check` в отдельном worktree от HEAD: только то, что реально в git.
# Ловит файлы, которые есть локально, но не закоммичены (gitignored embed и т.п.).
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
make -C "$tmp" check
