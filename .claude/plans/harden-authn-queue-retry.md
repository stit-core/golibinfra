---
status: in-progress
parent: go-backend-template:reaudit-to-nine
blocked-by: —
---

# harden-authn-queue-retry — хэшер отказывает при полной очереди, повтор refresh в окне не гасит сессию

## Цель
Закрыть в authnkit два пункта шаблона из программы `reaudit-to-nine` (трек g-auth, шаг S9): хэшер Argon2id при
переполнении очереди отказывает сразу, а не держит запрос до дедлайна (RSECT-01), а честный повтор refresh после сбоя
потребителя после `Rotate` или потери ответа не гасит сессию, и кража по-прежнему обнаруживается (RCOD-16). Итог уходит
потребителям тегом `v1.6.0`.

## Контекст
- Программа — `go-backend-template/.claude/plans/reaudit-to-nine.md` (шаг S9, раздел «golibinfra — S9», решение
  владельца 16.10): пункты бэклога шаблона `golibinfra-hash-queue` и `golibinfra-rotation-retry`. Ветка `nine/g-auth`,
  worktree `N/wt-g-auth` от `v1.5.0` (`90adb36`); `N` = `/home/mm/.cache/stit-audit-program/nine`, журнал хода —
  `N/g-auth/progress.md`. Коммиты на ветке — по шагу; push и тег — у дирижёра программы.
- Находки — `R/template-security.md § RSECT-01` (проба H2 — `R/tplsec-probes/cmd-api/raprobe_dos_test.go`, логи
  `R/tplsec-logs/argon*.log`) и `R/template-code.md § RCOD-16` (эксперимент E3 — `R/tpl-code/api_e1_e2_e3_e6_test.go.txt`),
  `R` = `/home/mm/.cache/stit-audit-program/reaudit`.
- Хэшер — `authnkit/hasher.go`: `max(1, GOMAXPROCS/Threads)` слотов, ожидание слота ограничено только ctx (`idKey`).
  Сессии — `authnkit/session` (контракт `Store`, `Config`) и `session/pgstore/store.go`: ротация — один условный
  оператор, `reject` отзывает сессию при любом повторе использованного токена. Фейки — `testkit/authntest`
  (`DeterministicHasher`) и `testkit/sessiontest` (фейк `Store` и контракт `RunContract`).
- Потребитель — шаблон (`go.mod`: `v1.5.0`): `internal/domain/auth/usecase.go` — `refresh` зовёт `Rotate`, затем
  `Users.GetByID` (сбой поиска после ротации — 500, повтор тем же cookie — `AUTH_REFRESH_REUSED`), `login` зовёт
  `VerifyDummy` на неизвестном и заблокированном адресе; источники миграций китов — `cmd/migrate/sources`. Шаблон правит
  трек g-upgrade после тега — по разделу «Handoff».
- Пробы кита на базе (`N/wt-g-auth-base` на `90adb36`, файлы `N/g-auth/probes/`, логи `N/g-auth/probe-*-base.log`):
  проверка пароля жертвы под потоком 40/с при `GOMAXPROCS=4` (хэш 47 мс) — 1.3 с → 2.5 с → дедлайн 3 с; повтор ротации
  тем же токеном через 2 с — `ErrReused`, сессия отозвана; три сценария кражи — `ErrReused`.
- Параллельно в golibinfra идут треки g-arch (`archkit/`) и g-kits (`httpkit/errormap.go`, `logkit/`): их файлы план
  не трогает.
- Валидация `9d96d98` — `N/validation-g-auth.md` (пробы `N/val-g-auth/`): FAIL по VGA-01 (high) — guard входа
  (`bruteforcekit`, шаблон `auth/usecase.go:81,233`) учитывает попытку до хэша, и отказ `ErrBusy` под потоком
  блокирует честного пользователя за пять повторов 503 (10 жертв: база — вошли все, ветка — 8–9 заблокированы на
  15 мин); minor VGA-02…06. Дирижёр расширил владение на `bruteforcekit/` и `testkit/bruteforcetest` (вариант (а)
  валидатора — возврат попытки) и включил minor в трек.
- Перепроверка `cc9beab` — PASS (`N/validation-g-auth.md § 9`, пробы `N/val-g-auth/probes/recheck/`) с двумя minor —
  пробелами тестов: VGA-07 — контракт сессий не ловит хранилище, у которого параллельные повторы оставляют несколько
  живых токенов (проба `zzracy`); VGA-08 — пол счётчика `Refund` не закреплён (мутант W3, проба R4). Журнал доводки —
  `N/g-auth3/progress.md`.

## Не входит
- Правки шаблона (трек g-upgrade), push и тег `v1.6.0` (дирижёр), аудит golibinfra и её собственный бэклог.
- Переменная глубины очереди хэшера и признак «повтор» у выданного `session.Token` — «Решения».

## Критерии приёмки
- [x] **AC1** — RSECT-01: при полной очереди Argon2id `Hash`, `Verify` и `VerifyDummy` отказывают сразу ошибкой
  `ErrBusy` с `RetryAfter()`, а не ждут слот до дедлайна запроса; проверка под потоком сильнее ёмкости хэшера ждёт не
  дольше очереди (девять хэшей на слот) или получает отказ сразу. Проверка: `go test -race -run 'TestArgon2id_FullQueue' ./authnkit/` → PASS; проба `zz_gauth_hash_probe_test.go` при `GOMAXPROCS=4` — база: секунды до дедлайна, ветка: не дольше 0.5 с или `ErrBusy` сразу
  - Подтверждено: `TestArgon2id_FullQueueFailsFast` → PASS (`-count=3 -race`); проба при `GOMAXPROCS=4` под потоком 40/с —
    база: 1.3 с → 2.5 с → дедлайн 3 с, ветка: `ErrBusy` за 0 с, потоку отказано 161 из 239 (`N/g-auth/probe-hash-*.log`)
- [x] **AC2** — отказ при полной очереди не выдаёт учётную запись: путь «учётной записи нет» (`VerifyDummy`) и пароль
  длиннее предела получают тот же `ErrBusy`, что `Verify`; фейк `authntest` даёт этот исход тестам потребителя.
  Проверка: `go test -race -run 'TestArgon2id_FullQueue|TestDeterministicHasher' ./authnkit/ ./testkit/authntest/` → PASS
  - Подтверждено: → PASS; в `TestArgon2id_FullQueueFailsFast` `VerifyDummy` и `Verify` пароля длиннее предела — `ErrBusy`
    (мутант «длинный пароль без ошибки холостого вычисления» убит), `TestDeterministicHasher_Busy` — тот же порядок исходов
- [x] **AC3** — RCOD-16: повтор `Rotate` тем же токеном не позже `SESSION_RETRY_WINDOW` (по умолчанию 30 с) после его
  ротации, пока преемник не использован, выдаёт нового преемника и гасит прежнего — сессия жива; повтор вне окна, после
  использования преемника и предъявление погашенного преемника — `*ReusedError` и отзыв сессии, как прежде. Проверка: `sessiontest.RunContract` на фейке и pgstore → PASS; проба `zz_gauth_session_probe_test.go` — база: повтор через 2 с — `ErrReused`, ветка: новый токен, три сценария кражи — `ErrReused`
  - Подтверждено: `TestStore_Contract` (pgstore) и `TestStore_Contract` фейка → PASS; проба — база: повтор через 2 с —
    `ErrReused`, сессия отозвана; ветка: новый токен, сессия жива, токен потерянного ответа — `ErrReused`, три сценария
    кражи — `ErrReused` (`N/g-auth/probe-session-*.log`); `TestStore_RetryAfterLostResponse` на базе — `ErrReused`
- [x] **AC4** — параллельные `Rotate` одним токеном в окне не оставляют двух живых токенов сессии. Проверка: `go test -race -run 'TestStore_ConcurrentRetry' ./authnkit/session/pgstore/` → PASS
  - Подтверждено: → PASS (`-count=3`, 20 раундов по 8 вызовов — из свежего и из использованного токена); мутант «повтор
    гасит преемника без `used_at IS NULL`» им убит (`N/g-auth/mut/pgstore-mutants.log`)
- [x] **AC5** — эксперименты аудитора на копии шаблона: H2 (лимит `/api/auth/*` снят) — вход жертвы на базе секунды,
  на ветке не дольше очереди или отказ сразу; E3 — на базе `AUTH_REFRESH_REUSED` и закрытая сессия, на ветке — 204,
  сессия открыта, записи `refresh_reused` нет. Проверка: логи `N/g-auth/tpl-*.log`
  - Подтверждено: копии `main` шаблона (`git archive`), проба `N/g-auth/probes/zz_gauth_exp_test.go`, `GOMAXPROCS=4`,
    память Argon2id 64 МиБ. H2 — база: вход жертвы 10.4 с и 24.6 с при 36 мс без потока, потоку 336 ответов 503 по
    дедлайну; ветка: 340 мс, затем отказ за 0 с (500 — `ErrBusy` в шаблоне ещё без правила), поток отклонён сразу
    (`tpl-*-h2.log`). E3 — база: `AUTH_REFRESH_REUSED`, сессий 0, `refresh_reused` 1; ветка: 204, сессия открыта,
    `refresh_reused` 0, старый cookie после использования преемника — `AUTH_REFRESH_REUSED` (`tpl-base.log`, `tpl-branch.log`)
- [x] **AC7** — VGA-01: попытка, разрешённая guard'ом и не дошедшая до проверки секрета, возвращается
  (`bruteforcekit.Guard.Refund`) — одинаково в pgstore и фейке, без гонок; на копии шаблона с правкой входа (возврат на
  `ErrBusy`) жертвы с верным паролем под потоком входят без блокировок, а перебор упирается в guard после
  `MaxFailures` проверенных попыток. Проверка: `bruteforcetest.RunContract` (фейк и pgstore), `TestStore_RefundConcurrent` → PASS; эксперимент валидатора `TestValExp_Lockout*` — база: блокировки, ветка: 0
  - Подтверждено: контракт и `TestStore_RefundConcurrent` → PASS (S5). Копии `main` шаблона, пробы валидатора
    (`TestValExp_Lockout*`, 10 жертв с верным паролем, повтор раз в секунду) и перебор под потоком
    (`N/g-auth/probes/zz_gauth_vga01_test.go`), `GOMAXPROCS=4`, Argon2id 64 МиБ (`N/g-auth/vga01.log`):
    `9d96d98` + правка валидатора (503 без возврата) — заблокированы 8, 8 и 9 из 10 (90/с 30 с, 40/с, 90/с 60 с), перебор
    заблокирован после одной проверенной попытки и четырёх отказов; HEAD + возврат на `ErrBusy` — заблокированных 0,
    вошли 7, 10 и 7 из 10 (p50 13.6 / 15.6 / 14.5 с; трое — без входа до конца потока: ёмкость хэшера, не guard),
    перебор — `AUTH_ACCOUNT_LOCKED` ровно после 5 проверенных попыток. v1.5.0: вошли 10, 10 и 0 (в долгом — дедлайн)
- [x] **AC8** — minor валидатора: два повтора подряд и срок токена повтора у конца сессии закреплены (мутанты V1, V3
  убиты), фейк при окне 0 и часах назад — как pgstore (V2 убит), параллельные повторы в окне фейк и pgstore проводят
  одинаково — все, живым остаётся один токен (VGA-04), пример «Логин» отвечает на отмену одинаково (VGA-05),
  `SESSION_RETRY_WINDOW` — не больше 5m и совет держать секунды (VGA-06). Проверка: `go test -race ./authnkit/session/... ./testkit/sessiontest/` → PASS, кейсы красные на `9d96d98`; мутанты V1–V3 → убиты
  - Подтверждено: → PASS; на `9d96d98` красные `StrictReuseAfterClockStepBack` (фейк), `ConcurrentRetriesKeepTheSession`
    и `TestStore_ConcurrentRetry` (pgstore), окно 6m проходит `Validate` (`N/g-auth/red-s6-base.log`, `red-s7-base.log`);
    мутанты V1–V5, M1–M7 убиты (`N/g-auth/mut/`); пример «Логин» — одна ветка на ошибку проверки у обоих путей (S5)
- [x] **AC6** — доки и сигналы китов по факту: README и `docs/` authnkit и bruteforcekit, строки testkit и корневого
  README, `ErrBusy` и ответ на него (503, `Retry-After`, возврат попытки guard'а), `authn_password_busy_total`,
  `result="retry"`, `SESSION_RETRY_WINDOW`; «Handoff» — строки аннотации `v1.6.0` и шаги шаблона, сверенные с
  `make release-diff`. Проверка: `make envdoc-check` → OK; `make release-diff PREV=v1.5.0` — каждая строка в «Handoff»
  - Подтверждено: `make envdoc-check` → OK (255 переменных); `make release-diff PREV=v1.5.0`
    (`N/g-auth/release-diff-s8.log`): четыре несовместимые строки API (`VerifyDummy` у `Hasher`, `*Argon2idHasher`,
    `authntest.DeterministicHasher`; `bruteforcekit.Guard.Refund`) — в Breaking; новое (`ErrBusy`, `Config.RetryWindow`,
    `MaxRetryWindow`, `Refund` у pgstore и фейков, `Recorder.Refunds`, `bruteforcetest.RunContract`,
    `DeterministicHasher.Busy`), `+ SESSION_RETRY_WINDOW=30s`, `+ authn_password_busy_total` — в Изменениях и
    Поведении; футеры `BREAKING CHANGE` коммитов S1, S2 и S5 — строки Breaking
- [x] **AC9** — VGA-07: `sessiontest.RunContract` снаружи, без доступа к устройству хранилища, проваливает хранилище,
  у которого параллельные повторы в окне оставляют сессии несколько живых токенов; фейк и pgstore контракт проходят.
  Проверка: `go test -race -run 'TestStore_Contract|TestRunContract_BrokenStoreFails' ./testkit/sessiontest/ ./authnkit/session/pgstore/` → PASS; наивные хранилища — красные в `ConcurrentRetriesLeaveOneLiveToken`
  - Подтверждено: → PASS; контракт `cc9beab` наивные хранилища (`RacyRetry` и `zzracy` валидатора) проходят целиком,
    новый — красный: ротируются 7 из 8 выданных (`N/g-auth3/vga07-red-green.log`); самотест `retry-races-its-check` —
    красный в 1100 прогонах из 1100, в том числе под нагрузкой CPU; мутант pgstore «повтор гасит преемника без
    `used_at IS NULL`» контракт `cc9beab` пропускал (ловил только `TestStore_ConcurrentRetry`), новый — красный
- [ ] **AC10** — VGA-08: возврат после параллельного успешного входа не уводит окно ниже нуля — субъект пропускает
  `SubjectMaxFailures` попыток, не больше, одинаково в фейке и pgstore. Проверка: `go test -race -run 'TestCounting_Contract|TestStore_Contract|TestRunContract_BrokenGuardFails' ./testkit/bruteforcetest/ ./bruteforcekit/pgstore/` → PASS; мутанты «пол снят» (W3 в pgstore, `max` фейка) — красные

## Шаги
- [x] **S1** — authnkit: очередь хэшера с отказом `ErrBusy` (RSECT-01) — вычисления ждут слот не дальше
  `queuePerSlot` на слот, лишние — `ErrBusy` сразу; `VerifyDummy` отдаёт ошибку; `authn_password_busy_total`; фейк
  `authntest` (`VerifyDummy` с ошибкой, поле `Busy`); доки кита и testkit
  - Проверка: `go test -short -race ./authnkit/ ./testkit/authntest/` → PASS, регрессия на базе красная; проба H на ветке; `make check` → `✓ check clean (./...)` · Закрывает: AC1, AC2, AC6
  - Сделано: слот ждут не больше 8 вызовов на слот, следующий — `ErrBusy` сразу (`RetryAfter()` — 1 с, ответ 503);
    `VerifyDummy` отдаёт ошибку, `Verify` длинного пароля — ошибку холостого вычисления; `authn_password_busy_total`,
    гистограммы — только допущенные вычисления; фейк — `VerifyDummy` с ошибкой и `Busy`; README и `docs/` кита, строки
    testkit и корневого README. `TestArgon2id_FullQueueFailsFast` → PASS, мутант «ожидание вместо отказа» убит; на базе
    регрессия красная — вызов ждал дедлайн 2 с (`N/g-auth/red-base.log`); `go test -short -race ./authnkit/...
    ./testkit/authntest/` → PASS; `make check` → `✓ check clean (./...)`
- [x] **S2** — authnkit/session: окно повтора ротации (RCOD-16) — `Config.RetryWindow` (`SESSION_RETRY_WINDOW`),
  миграция `0002` (`successor_hash`), pgstore и фейк, кейсы `RunContract`, `result="retry"`, доки
  - Проверка: `go test -race ./authnkit/session/... ./testkit/sessiontest/` → PASS, регрессия на базе красная; проба R на ветке; `make check` → `✓ check clean (./...)` · Закрывает: AC3, AC4, AC6
  - Сделано: `Config.RetryWindow` (`SESSION_RETRY_WINDOW`, 30s, `0 ≤ … < SESSION_IDLE_TTL`); миграция `0002`
    (`successor_hash`); ротация пишет преемника, повтор в окне — один оператор (`retrySQL`), прочее — `reject`; фейк — то
    же окно; контракт — семь кейсов окна и строгий режим, второй сломанный store (повтор не гасит преемника) проваливает
    `RetryKillsPreviousSuccessor`; `result="retry"`; регрессия `TestStore_RetryAfterLostResponse`, `TestStore_ConcurrentRetry`;
    доки кита, строки testkit, корневых README и SECURITY.md. Шесть мутантов SQL и пять мутантов фейка убиты
    (`N/g-auth/mut/`); `go test -race ./authnkit/session/... ./testkit/sessiontest/ ./internal/envconf/` → PASS;
    `make envdoc-check` → OK; `make check` → `✓ check clean (./...)`
- [x] **S3** — эксперименты аудитора H2 и E3 на копии шаблона (`git archive`): база `v1.5.0` и ветка через `replace`
  - Проверка: логи `N/g-auth/tpl-*.log` · Закрывает: AC1, AC3, AC5 · Зависит от: S1, S2
  - Сделано: H2 и E3 — по образцу аудитора (AC5); шаблон собирается на ветке без правок кода. Найдено для шаблона: без
    правила `ErrBusy` известный адрес под нагрузкой — 500, неизвестный — 401 сразу (`VerifyDummy` с ошибкой шаблон пока
    не читает); guard входа учитывает попытку до хэша, и клиент, повторяющий отказ раз в секунду, на десятой попытке
    получил 429 — «Handoff», п. 2. Доки: `Retry-After` из `RetryAfter()` — пока `ErrBusy` в цепочке ошибки ответа
- [x] **S4** — «Handoff» сверен с `make release-diff PREV=v1.5.0`; гейт на чистом дереве
  - Проверка: `make gate-clean` → `✓ check clean (./...)` · Закрывает: AC6 · Зависит от: S3
  - Сделано: «Handoff» сверен с `release-diff` построчно; тексты кита и контракта — граница окна («меньше чем через»),
    параллельные ротации в архитектуре; `make check` → `✓ check clean (./...)`; `make gate-clean` на коммите шага →
    `✓ check clean (./...)`

### Доводка по валидации
- [x] **S5** — bruteforcekit: возврат попытки без проверки секрета (VGA-01) — `Guard.Refund` в pgstore и фейках,
  контракт `bruteforcetest.RunContract`; доки bruteforcekit и authnkit: возврат на `ErrBusy`, пример «Логин» (VGA-05)
  - Проверка: `go test -race ./bruteforcekit/... ./testkit/bruteforcetest/` → PASS; `make check` → `✓ check clean (./...)` · Закрывает: AC7, AC8
  - Сделано: `Guard.Refund` — минус попытка окна субъекта и пары, блокировка ниже порога снята, истёкшая — нет; pgstore
    двумя операторами в порядке `Attempt`, фейки (`Counting` — с откатом `pgtest`, `Recorder.Refunds()`); контракт
    `RunContract` (семь кейсов) проходят `Counting` и pgstore, два сломанных возврата его проваливают;
    `TestStore_RefundConcurrent` (21 поток попыток и возвратов одной пары, `-count=10`) — счётчик равен попыткам без
    возврата, к проверке их не больше `MaxFailures`; `attemptRounds` 4 → 16 (под этим потоком 4 раунда не хватало).
    Доки: README bruteforcekit (поток входа, `Refund`, инварианты), authnkit (возврат на `ErrBusy`, пример «Логин» —
    отмена `ctx` на обоих путях одна, VGA-05), строки testkit и корневого README. На `9d96d98` возврата нет — тест не
    собирается; `make check` → `✓ check clean (./...)`
- [x] **S6** — сессии: параллельные повторы в окне проходят все (VGA-04), фейк в строгом режиме (VGA-03), кейсы двух
  повторов подряд и срока токена повтора (VGA-02)
  - Проверка: `go test -race ./authnkit/session/... ./testkit/sessiontest/` → PASS, кейсы на `9d96d98` красные; мутанты V1–V3 → убиты · Закрывает: AC8
  - Сделано: `retrySQL` отдаёт строку, когда родитель годен (преемник по снимку не использован), и пустую выдачу при
    проигранной гонке — `retry` идёт заново, до 16 кругов; фейк — окно 0 повтор не открывает; кейсы контракта
    `TwoRetriesInARow`, срок токена повтора у конца сессии (`RetryDoesNotOutliveSession`),
    `StrictReuseAfterClockStepBack`, `ConcurrentRetriesKeepTheSession`; `TestStore_ConcurrentRetry` — все повторы с
    токеном, живой ровно один, `TestStore_RetryOfMovedOnChainIsOneStatement`; доки — параллельные повторы по очереди.
    На `9d96d98` красные: фейк — `StrictReuseAfterClockStepBack`, pgstore — `ConcurrentRetriesKeepTheSession` и
    `TestStore_ConcurrentRetry` (`N/g-auth/red-s6-base.log`); мутанты валидатора V1–V3, свои V4–V5 и M1–M7 убиты
    (`N/g-auth/mut/vga-mutants.log`, `pgstore-mutants-s6.log`); `make check` → `✓ check clean (./...)`
- [x] **S7** — `SESSION_RETRY_WINDOW` не больше 5m и совет держать окно коротким (VGA-06)
  - Проверка: `go test -race ./authnkit/session/ ./internal/envconf/` → PASS · Закрывает: AC8
  - Сделано: `session.MaxRetryWindow` (5m) — потолок в `Validate` (ошибка называет переменную), строка
    `SESSION_RETRY_WINDOW` в `configuration.md` — держать секунды и почему; на `9d96d98` окно 6m `Validate` пропускал
    (`N/g-auth/red-s7-base.log`); `go test -race ./authnkit/session/ ./internal/envconf/` → PASS;
    `make check` → `✓ check clean (./...)`
- [x] **S8** — эксперимент VGA-01 на копии шаблона с правкой входа (возврат на `ErrBusy`), «Handoff», гейт на чистом
  дереве
  - Проверка: `TestValExp_Lockout*` на копиях; `make gate-clean` → `✓ check clean (./...)` · Закрывает: AC7, AC6 · Зависит от: S5, S6, S7
  - Сделано: три копии шаблона (`N/g-auth/tpl-v-base`, `tpl-v-old`, `tpl-v-fix`, `run-vga01.sh`) — итог в AC7; правка
    входа на `tpl-v-fix` — заготовка для g-upgrade: порт `LoginGuard.Refund`, адаптер, `refundOnBusy` на путях
    `Login` и `ChangePassword` поверх эмуляции валидатора (`ErrBusy` → 503 `AUTH_BUSY`). «Handoff» дополнен возвратом
    попытки и сверен с `release-diff`; строка покрытия README bruteforcekit; `make check` → `✓ check clean (./...)`;
    `make gate-clean` на коммите шага → `✓ check clean (./...)`

### Доводка по перепроверке
- [x] **S9** — контракт сессий: после параллельных повторов в окне живой токен один (VGA-07)
  - Проверка: `go test -race ./testkit/sessiontest/ ./authnkit/session/pgstore/` → PASS; наивные хранилища — красные в новом кейсе · Закрывает: AC9
  - Сделано: кейс `ConcurrentRetriesLeaveOneLiveToken` — восемь параллельных `Rotate` одним токеном, у каждого
    выданного — сессия и владелец исходного, затем выданные предъявляются с последнего завершённого: ротаций не больше
    одной; самотест `retry-races-its-check` — фейк с повтором, который проверяет и гасит преемника не под одной
    блокировкой (`RacyRetry`, `export_test.go`), контракт проваливает в этом кейсе; доки — `authnkit/docs/testing.md`,
    строка `sessiontest` README testkit; «Handoff» — строка `sessiontest`. Красное и зелёное — AC9;
    `go test -race ./testkit/sessiontest/ ./authnkit/session/pgstore/` → PASS; `make check` → `✓ check clean (./...)`
- [ ] **S10** — контракт возврата: пол счётчика `Refund` — кейс по пробе R4 валидатора (VGA-08)
  - Проверка: `go test -race ./testkit/bruteforcetest/ ./bruteforcekit/pgstore/` → PASS; мутанты «пол снят» — красные · Закрывает: AC10

## Решения
- RSECT-01 — отказ по длине очереди, а не по сроку ожидания: при полной очереди отказ приходит сразу и освобождает
  соединение и горутину, а ожидание по таймеру держало бы каждый лишний запрос весь срок; длина очереди на слот
  ограничивает ожидание принятого вызова (восемь хэшей впереди) при любом числе ядер.
- Глубина очереди — константа кита (8 на слот), без переменной: число слотов кит тоже выводит сам
  (`GOMAXPROCS/ARGON2_THREADS`); поле в `Argon2idConfig` смешало бы окно параметров PHC (`NeedsRehash` сравнивает
  структуру) с очередью, а ноль в литерале конфига у потребителей молча значил бы «без очереди». Настройка — когда
  потребителю её не хватит.
- Ответ на `ErrBusy` — 503 и `Retry-After`, а не 429: полная очередь — перегрузка процесса для всех клиентов, а не
  лимит клиента. Ошибка несёт `RetryAfter()` (1 с), и `httpkit.ErrorMap` ставит заголовок сам, пока `ErrBusy` в
  цепочке ошибки ответа; `ErrBusy` — значение своего типа с этим методом, поэтому фейк `authntest` отдаёт ту же ошибку,
  что боевой хэшер.
- `VerifyDummy` отдаёт ошибку (Breaking): без неё под нагрузкой путь «учётной записи нет» отвечал бы 401 сразу, а
  известный адрес — 503, и отказ различал бы учётные записи. Пароль длиннее предела — так же: `Verify` отдаёт ошибку
  холостого вычисления, а не `ErrPasswordTooLong`.
- Гистограммы времени хэшера — от допуска в очередь до конца вычисления: отказ пишет только
  `authn_password_busy_total`, иначе нули отказов тянули бы квантили вниз как раз под нагрузкой.
- RCOD-16 — окно повтора, а не чтение владельца до ротации: чтение до `Rotate` закрывает только сбой потребителя до
  ротации, а не потерю ответа в сети и не сбой после неё (cookie, `IssueAccess`), и добавляет второй способ обновления с
  гонкой между чтением и ротацией. Окно закрывает все случаи одним механизмом кита, без правок потребителя.
- Повтор в окне выдаёт нового преемника и гасит прежнего (живым остаётся один), а не того же преемника: тот же
  преемник требует хранить его открытым или выводить из токена (своя криптосхема) и даёт вору в окне общий с клиентом
  токен — вор, повторяющий каждую ротацию клиента в окне, не обнаруживается. С погашением кто бы ни повторил, второй
  участник получит `*ReusedError` на следующем обновлении.
- Что получает вор: использованный токен, предъявленный в окне после законной ротации, даёт ему живого преемника
  вместо отзыва сессии и гасит преемника клиента — следующее обновление клиента погашенным токеном отзывает сессию
  вместе с токеном вора. Обнаружение откладывается до следующего обновления клиента, но не теряется; вор, опередивший
  клиента, — тот же случай, что без окна. Окно не продлевает сессию и не открывает отозванную: повтор требует живую
  сессию и неиспользованного преемника.
- Аудит и метрики: честный повтор — `authn_session_rotations_total{result="retry"}`, не `reused`; `*ReusedError` (и
  аудит потребителя `refresh_reused`) — повтор вне окна, после использования преемника и погашенный преемник: алерт на
  `reused` не срабатывает на одиночный повтор клиента, параллельное обновление по-прежнему видно. Признака «повтор» у
  выданного `session.Token` нет: ответ клиенту от него не зависит, сигнал — `result="retry"`; аудит повторов — когда
  он понадобится потребителю.
- `Rotate` по-прежнему вне транзакции вызывающего (`ErrInTx`): отзыв при повторе не должен откатываться вместе с ней —
  поэтому потребитель и не может откатить ротацию при своём сбое. Окно — компенсация на стороне кита: повтор — отдельный
  атомарный оператор (гасит преемника, вставляет нового, переставляет указатель родителя), транзакции он не требует.
- `successor_hash` у родителя, а не `parent_hash` у преемника: преемник — по первичному ключу, нового индекса нет.
- VGA-04 — параллельные повторы в окне проходят все, по очереди, в фейке и pgstore одинаково: оператор повтора,
  чей снимок видел преемника, которого после снимка погасил другой повтор, идёт заново (до 16 кругов) и гасит
  преемника победителя. Живым остаётся один токен — последний выданный; честные параллельные обновления (вкладки)
  сессию не закрывают сразу, а клиент, оставивший себе не последний токен, получит `*ReusedError` на следующем
  обновлении — без повтора того же преемника (криптосхема) лучше не сделать. Отказ остаётся отказом: повтор, для
  которого цепочка ушла дальше, — один оператор и отзыв (`TestStore_RetryOfMovedOnChainIsOneStatement`); проигрыш —
  всегда чужой выигрыш, поэтому круги ограничены числом одновременных повторов. Транзакция с `FOR UPDATE` родителя
  (вариант валидатора) потребовала бы у `Store` `Transactor` — смену конструктора ради того же исхода.
- `SESSION_RETRY_WINDOW` — 30 с по умолчанию, окно `0 ≤ … < SESSION_IDLE_TTL`: покрывает повтор после 5xx и обрыва
  соединения; ноль в литерале `Config` — строгий режим без окна, литералы потребителей поведения молча не меняют.
- Повтор в окне у отозванной или истёкшей сессии — `*ReusedError`, как у любого использованного токена: порядок исходов
  прежний, окно меняет только случай «сессия жива, преемник не использован» (кейсы `RetryDoesNot…` контракта).
- Контракт `RunContract` берёт `Config` кейса: окно — минута, строгие кейсы (`Strict…`, `ConcurrentRotateHasOneWinner`)
  — без окна.
- «Живой токен после параллельных повторов — один» контракт сессий проверяет снаружи (`ConcurrentRetriesLeaveOneLiveToken`):
  выданные предъявляются с последнего завершённого вызова, ротаций — не больше одной. Ровно одной требовать нельзя:
  порядок завершения вызовов не обязан совпадать с порядком записи, а первый же погашенный токен отзывает сессию; у
  наивного хранилища живые — токены перекрывшихся повторов, они завершаются последними и идут первыми. «Ровно один»
  pgstore держит ещё тестом по базе (`TestStore_ConcurrentRetry`).
- Наивное хранилище самотеста сессий — фейк с повтором, который проверяет и гасит преемника не под одной блокировкой
  (`RacyRetry` в `export_test.go`, белый ящик), а не обёртка над фейком: через его API два живых токена одной сессии не
  получить — ротация атомарна. Окно между проверкой и записью — 20 мс: перекрытие повторов не зависит от планировщика.
- Чужие файлы: строки authnkit (корневые `README.md`, `SECURITY.md`) и bruteforcekit (`README.md`) — о своих китах.
- VGA-01 — возврат попытки методом `Guard.Refund`, а не билетом из `Attempt`: билет меняет сигнатуру `Attempt` у
  каждого вызывающего, а возврат нужен одному пути — отказу проверки. Семантика: минус одна попытка окна пары и
  субъекта (не ниже нуля); блокировка снимается, только если счётчик упал ниже порога, — её поставила на пороге эта или
  параллельная разрешённая попытка, и проверенных в окне от этого не больше `MaxFailures`; истёкшую блокировку возврат
  не трогает — иначе следующая попытка продолжила бы старое окно на пороге; стёртую успехом историю не создаёт.
  Безопасность — правило вызова: возврат только непроверенной попытки (`authnkit.ErrBusy`); перебор от возвратов
  лишних проверок не получает (кейс `RefundKeepsCheckedAttempts`), возврат проверенной дал бы — это названо в доках.
- `Refund` — метод `Guard` (request-path рядом с `Attempt` и `RecordSuccess`), а не отдельный интерфейс: Breaking для
  своих реализаций `Guard` у потребителей, шаблон оборачивает `Guard`, а не реализует его. В транзакции допустим, как
  `RecordSuccess`: откат оставляет попытку учтённой (fail-closed). Метрик возврат не заводит: его темп виден по
  `authn_password_busy_total`, блокировка, снятая возвратом, остаётся в `bruteforce_locks_total` — названо в доках.
- `attemptRounds` pgstore — 16 (было 4): возврат на пороге снимает блокировку, и гонка «upsert видел блокировку, чтение
  — уже нет» под потоком попыток и возвратов одной пары чаще (стресс: пятый раунд — единицы из тысяч, `Attempt` падал
  `rows changed` — fail-closed 500 честному клиенту); 16 — запас на порядки, предел цикла остаётся.
- Контракт `bruteforcetest.RunContract` — новый: возврат обязан вести себя одинаково в pgstore и фейке; кейсы без
  сдвига часов (pgstore считает время часами базы), истечение блокировки — тесты хранилищ.

## Handoff
### Аннотация `v1.6.0` — authnkit и bruteforcekit
Breaking:
- authnkit: `Hasher.VerifyDummy` и `(*Argon2idHasher).VerifyDummy`: `(ctx)` → `(ctx) error` — ошибку обработать как
  ошибку `Verify` (ответ тот же); своя реализация `Hasher` — новая сигнатура.
- authnkit: `Hash`, `Verify`, `VerifyDummy` при полной очереди Argon2id — `ErrBusy` сразу (было: ожидание слота до
  дедлайна ctx) → правило `errors.Is(err, authnkit.ErrBusy)` → 503 на всех путях пароля (вход по известному и
  неизвестному адресу, регистрация, смена и сброс), `Retry-After` — из `RetryAfter()` ошибки в цепочке; без правила — 500.
  Попытку guard'а входа, учтённую до проверки, на `ErrBusy` вернуть — `bruteforcekit.Guard.Refund`, затем 503: иначе
  повторы 503 под потоком блокируют честного пользователя за `BRUTEFORCE_MAX_FAILURES` отказов.
- bruteforcekit: `Guard` + `Refund(ctx, subject, ip) error` — своя реализация `Guard` добавляет метод.
- testkit/authntest: `DeterministicHasher.VerifyDummy` отдаёт `error`.
- authnkit/session/pgstore: миграция `0002` источника `authnkit_session` (`successor_hash` в `authn_session_tokens`) —
  до старта новой версии: без неё старт — `pgkit.ErrSchema`; `pgkit.MigrateModule` в графе применяет её сам.
- testkit/sessiontest: `RunContract` проверяет окно повтора — своя реализация `session.Store` должна его держать, в том
  числе один живой токен после параллельных повторов (повтор проверяет и гасит преемника атомарно).

Поведение:
- authnkit/session: `SESSION_RETRY_WINDOW` = 30s — повтор использованного refresh-токена в окне после его ротации,
  пока преемник не использован, выдаёт нового преемника и гасит прежнего, а не отзывает сессию (параллельные повторы —
  по очереди, живым остаётся последний выданный); `0` — прежняя строгость.
- authnkit/session/pgstore: `authn_session_rotations_total{result="retry"}` — честный повтор, `reused` его не считает.
- authnkit: гистограммы `authn_password_*_duration_seconds` пишут только допущенные в очередь вычисления.

Изменения:
- authnkit: `ErrBusy` (`RetryAfter()` — 1 с), метрика `authn_password_busy_total`.
- authnkit/session: `Config.RetryWindow` (`SESSION_RETRY_WINDOW`), потолок `MaxRetryWindow` — 5m.
- testkit/authntest: `DeterministicHasher.Busy` — `ErrBusy` у `Hash`, `Verify` и `VerifyDummy`.
- bruteforcekit/pgstore: `(*Store).Refund`; testkit/bruteforcetest: `Refund` у фейков, `Recorder.Refunds()`, контракт
  `RunContract(t, newGuard)`.

### Потребителю — шаблон (трек g-upgrade)
1. `go get …/golibinfra@v1.6.0`; миграция `0002` кита идёт командой миграций (`cmd/migrate/sources`), без эффекта
   (`ADD COLUMN` без значения по умолчанию).
2. `auth` — вместе с bump, иначе под нагрузкой известный адрес отвечает 500, а неизвестный 401 сразу: ошибка
   `VerifyDummy` — как ошибка `Verify`; `authnkit.ErrBusy` на путях пароля (`Login`, `ChangePassword`, `user.Register`,
   сброс пароля `verification`) → ошибка домена с 503 и `Retry-After` (ошибка кита — в цепочке); правило в таблицах
   операций, `make gen` (`openapi.json`); тесты — `authntest.DeterministicHasher{Busy: true}` на известном и неизвестном
   адресе. Возврат попытки: порт `LoginGuard` + `Refund` (адаптер `infra/bruteforce` зовёт `Guard.Refund`); `Login` —
   на `ErrBusy` известного (`Verify`), неизвестного и заблокированного адреса (`VerifyDummy`), `ChangePassword` — на
   `ErrBusy` проверки старого пароля: `Refund(ctx, email, ip)`, затем 503; тесты — `bruteforcetest.NewCounting`: пять
   `ErrBusy` подряд пару не блокируют, `Recorder.Refunds()` — возврат с ключом клиента; `make gen` — мок `LoginGuard`.
3. Повтор refresh: `TestSessions` (`cmd/api`) ждёт 401 на немедленный повтор и одного победителя параллельных обновлений,
   smoke `test/smoke/test_auth.py` — 401 на повтор погашенного cookie: на окне по умолчанию немедленный повтор — 204
   (прежний преемник погашен), параллельные обновления — все 204 по очереди, сессия жива. Кейсы — без окна
   (`SESSION_RETRY_WINDOW=0`) или повтор после использования преемника, плюс кейс окна (потерянный ответ → повтор 204 →
   cookie потерянного ответа — `AUTH_REFRESH_REUSED`); `usecase_test.go` (`auth`) — литерал `session.Config` без окна,
   не меняется.
4. Описание операции `refreshToken`, `SECURITY.md` (лимит входа), `docs/stack/_templates/auth.md`,
   `docs/reference/architecture.md` (повтор refresh в окне — не кража; алерт `reused` на одиночный повтор не срабатывает),
   `.env.example` (`SESSION_RETRY_WINDOW`: секунды, потолок 5m), якоря `scripts/tools/mutants.txt` у правленого кода.

## Где мы сейчас
Шаги S1–S8 и критерии AC1–AC8 закрыты, перепроверка `cc9beab` — PASS с двумя minor-пробелами тестов. S9 закрыт:
контракт сессий проваливает хранилище, у которого параллельные повторы оставляют несколько живых токенов (VGA-07).
Дальше — S10 (VGA-08): кейс контракта `bruteforcetest` по пробе R4 — возврат после параллельного успешного входа не
уводит окно ниже нуля; затем дирижёр: слияние в `main`, удаление плана, тег `v1.6.0` с аннотацией из «Handoff» и трек
g-upgrade шаблона (заготовка правки входа — `N/g-auth/tpl-v-fix/internal/domain/auth/`).
