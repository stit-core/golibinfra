---
name: release
description: Выпустить новую версию golibinfra — make gate-clean, аннотированный semver-тег vX.Y.Z с release notes + push, затем гайд обновления консьюмера (go get + go mod tidy). Активируй когда юзер говорит "/release", "релизни golibinfra", "подними версию", "выпусти vX.Y.Z", "затегай новую версию lib'ы".
---

# Skill: release

Версия golibinfra — git-тег `vX.Y.Z`. Реестр модулей — сам Gitea (`git.stit.tech`);
отдельной сборки/артефакта нет — консьюмеры тянут модуль по тегу. Release notes
живут в аннотации тега (`git tag -a`), а не в файле репозитория.

## Когда применять

- "Релизим vX.Y.Z" / "подними версию golibinfra" / "/release".
- После закрытой задачи и зелёного gate, когда пользователь сказал «пора».

## Когда НЕ применять

- Локальная разработка с одновременной правкой консьюмера → `replace`-директива в
  `go.mod` консьюмера (см. `README.md § Локальная разработка`), не тег.

## Шаги

1. **Gate на чистом дереве.** `make gate-clean` — `make check` в worktree от HEAD
   (ловит незакоммиченные и gitignored-файлы, от которых зависит сборка).
   Красное → не релизим. Для китов с БД/шиной дополнительно `make test-full`.
2. **Версия (semver).** Pre-v1.0.0: **minor может быть breaking**. Breaking change
   (коммиты с `!` / `BREAKING CHANGE:` с прошлого тега) → bump minor (`v0.X.0`);
   только фиксы/добавления → patch (`v0.X.Y`). Список коммитов с прошлого тега:
   `git log $(git describe --tags --abbrev=0)..HEAD --oneline`.
   Спроси у пользователя версию, если неочевидно.
3. **Release notes в аннотации тега.** Обязательные секции: `Breaking` (что
   сломано и как мигрировать, по китам), `Изменения` (кратко), `Гейт` (что гонялось).
   Пуш — гейт человека, делаем по явной просьбе:
   ```bash
   git tag -a vX.Y.Z -F - <<'EOF'
   vX.Y.Z

   Breaking:
   - httpkit: MapSentinel → MapIs/MapAs; RegisterHealth(api, log, probes...)
   - ...

   Изменения:
   - ...

   Гейт: make gate-clean, make test-full (Docker).
   EOF
   git push origin vX.Y.Z
   ```
4. **Следующий шаг — консьюмеру.** Напечатай блок обновления для `<org>/backend`
   (форк `go-backend-template`) вместе с миграционными пунктами из `Breaking`:
   ```bash
   go get git.stit.tech/stit-core/golibinfra@vX.Y.Z
   go mod tidy
   make check        # обязательный gate в консьюмере
   ```

## Нет тупика

Скилл всегда заканчивается явным следующим шагом: либо блок `go get` для консьюмера
с миграционными пунктами, либо (если gate красный) — что чинить перед релизом.
