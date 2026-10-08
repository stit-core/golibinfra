#!/usr/bin/env bash
# Шаг гейта lint (make lint, из корня репо). До прогона проверяет сам конфиг: схему — узел не на
# своём месте golangci-lint run молча пропускает — и набор включённых линтеров и форматтеров
# против эталона scripts/linters.txt.
#
#   scripts/lint.sh [pkg…]
set -euo pipefail

golangci-lint config verify

enabled() { # имена массива Enabled ответа `golangci-lint <linters|formatters> --json`
	golangci-lint "$1" --json | sed 's/"Disabled":.*//' | { grep -o '"name":"[^"]*"' || true; } | cut -d'"' -f4
}
want=$(grep -v '^#' scripts/linters.txt | sort)
got=$({ enabled linters && enabled formatters; } | sort) || {
	echo "FAIL: golangci-lint не отдал набор включённых линтеров"
	exit 1
}
if [[ $want != "$got" ]]; then
	echo "FAIL: набор линтеров .golangci.yml расходится с эталоном scripts/linters.txt:"
	comm -3 <(echo "$want") <(echo "$got") | sed -e $'s/^\t/  вне эталона: /' -e t -e 's/^/  снят: /'
	exit 1
fi

golangci-lint run "$@"
