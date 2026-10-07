// Package natsname — правила имён NATS, общие для natskit и его фейка testkit/natstest: subject
// публикации и фильтр подписки, имя стрима и консьюмера JetStream, queue-группа.
package natsname

import (
	"slices"
	"strings"
)

const whitespace = " \t\r\n"

// Subject — subject публикации: непустые токены, без пробельных символов и wildcard'ов (`*`, `>`
// осмысленны только в подписке; сервер такую публикацию отбрасывает молча).
func Subject(s string) bool {
	if s == "" || strings.ContainsAny(s, whitespace+"*>") {
		return false
	}
	return !slices.Contains(strings.Split(s, "."), "")
}

// Filter — фильтр подписки: непустые токены без пробельных символов, `>` — только последним токеном.
func Filter(s string) bool {
	if s == "" || strings.ContainsAny(s, whitespace) {
		return false
	}
	tokens := strings.Split(s, ".")
	for i, t := range tokens {
		if t == "" || t == ">" && i != len(tokens)-1 {
			return false
		}
	}
	return true
}

// Name — имя стрима или консьюмера JetStream: непустое, без `.`, `*`, `>`, `/`, `\` и пробельных.
func Name(s string) bool {
	return s != "" && !strings.ContainsAny(s, whitespace+".*>/\\")
}

// Queue — имя queue-группы: без пробельных символов.
func Queue(s string) bool {
	return !strings.ContainsAny(s, whitespace)
}
