---
name: comments
description: Комментарии — только короткие и про WHY. Нельзя срать многословной нарративной документацией в коде. Грузится при правке любых .go-файлов.
paths:
  - "**/*.go"
---

# Комментарии

**Default — не писать комментарий.** Хорошее имя функции/типа уже объясняет WHAT. Комментарий допустим, только если без него читатель не догадается о **WHY** — скрытая инварианта, неочевидное ограничение, workaround под конкретный баг, поведение которое удивит.

## Запрещено

- **Restate WHAT.** `// returns user by id` над `GetUserByID` — нет.
- **Narration.** Многострочный пересказ "сначала делаем X, потом Y, наконец Z". Код это и так показывает.
- **Архитектурные эссе в package docstring.** Не описывай pattern/принципы рядом с реализацией — это место для `README.md` или `CLAUDE.md`. Особенно касается `package module` файлов.
- **Tombstones.** `// removed XXX`, `// was using Y before` — нет, для этого есть git log.
- **Reference to current task/PR.** `// added for X flow`, `// fix for issue #123` — это в commit message / PR description, не в коде.
- **Flavor commentary.** Художественный пересказ того, что и так видно.
- **TODO без контекста.** `// TODO: fix this` — бесполезно. Оставляешь TODO — дай ticket/owner/condition.
- **Section dividers.** `// ===== Helpers =====` — Go-файлы и так структурированы.

## Допустимо (одна короткая строка)

- WHY-инвариант: `// Alias (not new type) so X satisfies Y structurally.`
- Workaround: `// pgx returns []byte for jsonb; cast manually until v5.7 fix.`
- Сюрпризное поведение: `// MUST be called before Start — registers handlers in fx graph.`
- Хитрая константа: `// 90s = upstream nginx idle timeout − 10s safety margin.`

## Чек

Больше **одной строки** — почти наверняка лишнее: вырази именем/структурой, а «почему так» — в `README.md`, не в `.go`. После правки глянь diff: добавленный комментарий объясняет невидимое из кода? Нет — удали.
