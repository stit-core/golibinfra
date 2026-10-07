// Package jsonb — что Postgres JSONB не хранит из JSON-текста encoding/json: auditkit отвергает
// такую Metadata до базы, фейк testkit/outboxtest — тело события, как его отверг бы Postgres.
package jsonb

import (
	"strconv"
	"unicode/utf8"
)

// Unstorable — почему Postgres JSONB не примет JSON-текст js (вывод encoding/json): «is not valid
// UTF-8», «contains NUL» (`\u0000`) или «has an unpaired surrogate»; "" — примет.
func Unstorable(js []byte) string { return problem(js, false) }

// UnstorableOrReplaced — Unstorable и ещё `\ufffd`: им encoding/json заменил битые байты строки (сам
// символ U+FFFD он пишет байтами) — запись с ним исказила бы данные молча.
func UnstorableOrReplaced(js []byte) string { return problem(js, true) }

// problem: обратный слеш в выводе encoding/json бывает только внутри строки.
func problem(js []byte, replaced bool) string {
	if !utf8.Valid(js) {
		return "is not valid UTF-8"
	}
	for i := 0; i < len(js); i++ {
		if js[i] != '\\' {
			continue
		}
		i++ // экранированный символ: `\\` — сам слеш, `\u` — код
		code, ok := escapedCode(js, i)
		if !ok {
			continue
		}
		switch {
		case code == 0:
			return "contains NUL"
		case replaced && string(js[i+1:i+5]) == "fffd":
			return "is not valid UTF-8"
		case code >= 0xDC00 && code <= 0xDFFF:
			return "has an unpaired surrogate"
		case code >= 0xD800 && code <= 0xDBFF:
			low, ok := escapedCode(js, i+6)
			if !ok || js[i+5] != '\\' || low < 0xDC00 || low > 0xDFFF {
				return "has an unpaired surrogate"
			}
			i += 6
		}
		i += 4
	}
	return ""
}

// escapedCode — код escape `\uXXXX`, чей `u` стоит на позиции i.
func escapedCode(js []byte, i int) (uint64, bool) {
	if i+4 >= len(js) || js[i] != 'u' {
		return 0, false
	}
	code, err := strconv.ParseUint(string(js[i+1:i+5]), 16, 16)
	return code, err == nil
}
