#!/usr/bin/env bash
# Каждый envconfig-тег в коде кита должен упоминаться в его README.md или docs/*.md.
# Обратное (переменная в доках, которой нет в коде) тоже ошибка.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

status=0
all_code=$(find . -name '*.go' ! -name '*_test.go' -not -path './.git/*' -exec grep -hoE 'envconfig:"[A-Z0-9_]+"' {} + | sed 's/envconfig:"//; s/"//' | sort -u)

for dir in */; do
	kit=${dir%/}
	[ -f "$kit/README.md" ] || continue
	code=$(find "$kit" -name '*.go' ! -name '*_test.go' -exec grep -hoE 'envconfig:"[A-Z0-9_]+"' {} + 2>/dev/null | sed 's/envconfig:"//; s/"//' | sort -u)
	docs=$(cat "$kit"/README.md "$kit"/docs/*.md 2>/dev/null | grep -oE '`[A-Z][A-Z0-9_]{2,}`' | tr -d '`' | sort -u)

	missing=$(comm -23 <(printf '%s\n' "$code" | sed '/^$/d') <(printf '%s\n' "$docs" | sed '/^$/d') | tr '\n' ' ')
	if [ -n "$missing" ]; then
		echo "envdoc: $kit: в коде, но не в доках кита: $missing"
		status=1
	fi

	# Переменные в доках кита, которых нет ни в одном ките репо (стейл).
	stale=$(comm -23 <(printf '%s\n' "$docs" | sed '/^$/d' | grep -E '_[A-Z]' ) <(printf '%s\n' "$all_code") | tr '\n' ' ')
	if [ -n "$stale" ]; then
		echo "envdoc: $kit: в доках, но не в коде: $stale"
		status=1
	fi
done

exit $status
