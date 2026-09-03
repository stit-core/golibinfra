#!/usr/bin/env bash
# Каждый envconfig-тег в коде кита должен упоминаться в его README.md или docs/*.md.
# Обратное (переменная в доках, которой нет ни в одном ките) тоже ошибка.
set -u

root=$(git rev-parse --show-toplevel)
cd "$root" || exit 1

tags_in() {
	# envconfig-теги из non-test .go под каталогом $1; пусто — не ошибка.
	find "$1" -name '*.go' ! -name '*_test.go' -exec grep -hoE 'envconfig:"[A-Z0-9_]+"' {} + 2>/dev/null |
		sed 's/envconfig:"//; s/"//' | sort -u
}

vars_in_docs() {
	# `UPPER_SNAKE`-токены с подчёркиванием из README и docs кита $1.
	cat "$1"/README.md "$1"/docs/*.md 2>/dev/null |
		grep -oE '`[A-Z][A-Z0-9]*(_[A-Z0-9]+)+`' | tr -d '`' | sort -u
}

status=0
allow=$(grep -vE '^\s*(#|$)' scripts/envdoc-allow.txt 2>/dev/null | sort -u)
all_code=$(printf '%s\n%s\n' "$(tags_in .)" "$allow" | sed '/^$/d' | sort -u)

for dir in */; do
	kit=${dir%/}
	[ -f "$kit/README.md" ] || continue
	code=$(tags_in "$kit")
	docs=$(vars_in_docs "$kit")

	missing=$(comm -23 <(printf '%s\n' "$code" | sed '/^$/d') <(printf '%s\n' "$docs" | sed '/^$/d') | tr '\n' ' ')
	if [ -n "$missing" ]; then
		echo "envdoc: $kit: в коде, но не в доках кита: $missing"
		status=1
	fi

	stale=$(comm -23 <(printf '%s\n' "$docs" | sed '/^$/d') <(printf '%s\n' "$all_code" | sed '/^$/d') | tr '\n' ' ')
	if [ -n "$stale" ]; then
		echo "envdoc: $kit: в доках, но не в коде: $stale"
		status=1
	fi
done

exit $status
