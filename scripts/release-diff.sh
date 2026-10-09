#!/bin/bash -p
# Что изменилось для потребителя от тега до рабочего дерева (make release-diff PREV=vX.Y.Z):
# API пакетов модуля — apidiff, env-переменные с дефолтами — scripts/envdoc, имена метрик —
# литералы в конструкторах инструментов OTel. По этому списку skill /release пишет аннотацию тега:
# несовместимая строка API, снятая переменная и снятое имя метрики — Breaking с шагом миграции,
# сменившийся дефолт — Поведение, новое — Изменения. Breaking, не помеченный `!` в коммите, поиск
# по истории не находит.
#
#   scripts/release-diff.sh <прошлый тег>
set -euo pipefail
# comm ждёт порядок sort своей локали, а envdoc сортирует по байтам.
export LC_ALL=C

[ -n "${1-}" ] || { echo "release-diff: нужен прошлый тег — make release-diff PREV=vX.Y.Z" >&2; exit 2; }
prev=$(git rev-parse --verify --quiet "$1^{commit}") || { echo "release-diff: нет ревизии $1" >&2; exit 2; }
module=$(go list -m)

work=$(mktemp -d "${TMPDIR:-/tmp}/golibinfra-release-diff.XXXXXX")
cleanup() {
	git worktree remove --force "$work/prev" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
git worktree add --detach --quiet "$work/prev" "$prev"

# «-» — было в прошлой версии, «+» — стало; входы отсортированы.
changes() { comm -3 "$1" "$2" | sed -e $'s/^\t/+ /' -e t -e 's/^/- /'; }

(cd "$work/prev" && apidiff -m -w "$work/prev.api" "$module")
apidiff -m "$work/prev.api" "$module" >"$work/api" 2>"$work/api.err" || { cat "$work/api.err" >&2; exit 1; }
# Не API: инструменты гейта (scripts/) и internal/ потребитель не импортирует.
api() {
	awk -v want="$1" '/ changes:$/ { on = ($0 == want); next } on && NF' "$work/api" |
		grep -vE "^- (package $module/|\./)(scripts|internal)([/.:]|$)" | sort || [ $? = 1 ]
}
echo "== API: несовместимо с $1 — каждой строке строка Breaking"
api "Incompatible changes:"
echo "== API: новое — раздел «Изменения»"
api "Compatible changes:"

go build -o "$work/envdoc" ./scripts/envdoc
(cd "$work/prev" && "$work/envdoc" -vars) >"$work/env.prev"
"$work/envdoc" -vars >"$work/env.now"
echo "== env, «имя=default»: «-» — было в $1, «+» — стало"
changes "$work/env.prev" "$work/env.now"

# Имя — первый аргумент конструктора: на его строке или на следующей.
metrics() {
	(cd "$1" && { grep -rhE -A1 --include='*.go' --exclude='*_test.go' --exclude-dir=testdata --exclude-dir='.[!.]*' \
		'\.(Int64|Float64)(Observable)?(Counter|UpDownCounter|Histogram|Gauge)\(' . || [ $? = 1 ]; }) |
		awk '
			match($0, /(Int64|Float64)(Observable)?(Counter|UpDownCounter|Histogram|Gauge)\(/) {
				kind = substr($0, RSTART, RLENGTH - 1); rest = substr($0, RSTART + RLENGTH)
				if (rest ~ /^[ \t]*$/) { getline; rest = $0 }
				if (match(rest, /^[ \t]*"[^"]+"/)) { name = substr(rest, RSTART, RLENGTH); gsub(/[ \t"]/, "", name); print name, kind }
			}' | sort -u
}
metrics "$work/prev" >"$work/metrics.prev"
metrics . >"$work/metrics.now"
echo "== метрики, «имя вид»: «-» — было в $1, «+» — стало"
changes "$work/metrics.prev" "$work/metrics.now"
