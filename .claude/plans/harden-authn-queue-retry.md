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
- [x] **AC6** — доки и сигналы кита по факту: README и `docs/` authnkit, строки testkit и корневого README, `ErrBusy` и
  ответ на него (503 и `Retry-After`), `authn_password_busy_total`, `result="retry"`, `SESSION_RETRY_WINDOW`; «Handoff» —
  строки аннотации `v1.6.0` и шаги шаблона, сверенные с `make release-diff`. Проверка: `make envdoc-check` → OK; `make release-diff PREV=v1.5.0` — каждая строка в «Handoff»
  - Подтверждено: `make envdoc-check` → OK (255 переменных); `make release-diff PREV=v1.5.0` — три несовместимые строки
    API (`VerifyDummy` у `Hasher`, `*Argon2idHasher`, `authntest.DeterministicHasher`) — в Breaking, новое (`ErrBusy`,
    `Config.RetryWindow`, `DeterministicHasher.Busy`), `+ SESSION_RETRY_WINDOW=30s` и `+ authn_password_busy_total` — в
    Изменениях и Поведении; футеры `BREAKING CHANGE` коммитов S1 и S2 — строки Breaking

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
- `successor_hash` у токена-родителя, а не `parent_hash` у преемника: преемник ищется по первичному ключу, нового
  индекса нет, обновление `used_at` при ротации остаётся HOT.
- Повтор, проигравший гонку другому повтору (параллельные запросы с одним токеном в окне), — `*ReusedError` и отзыв:
  живым остаётся не больше одного токена, клиент по-прежнему обновляет последовательно. Фейк такие вызовы проводит по
  очереди — каждый гасит преемника предыдущего; расхождение названо в доках.
- `SESSION_RETRY_WINDOW` — 30 с по умолчанию, окно `0 ≤ … < SESSION_IDLE_TTL`: покрывает повтор после 5xx и обрыва
  соединения; ноль в литерале `Config` — строгий режим без окна, литералы потребителей поведения молча не меняют.
- Повтор в окне у отозванной или истёкшей сессии — `*ReusedError`, как у любого использованного токена, а не
  `ErrInvalid`/`ErrExpired`: порядок исходов прежний (использованный токен — первым), окно не меняет ничего, кроме
  случая «сессия жива, преемник не использован». Честный повтор после «выйти везде» или на абсолютном сроке редок,
  а исход контракт держит кейсами `RetryDoesNotReopenClosedSession` и `RetryDoesNotOutliveSession`.
- Контракт `RunContract` берёт `Config` кейса: окно — минута, строгие кейсы (`StrictReuseRevokesSession`,
  `ConcurrentRotateHasOneWinner`) — без окна; гонку повторов в окне pgstore держит своим тестом по базе
  (`TestStore_ConcurrentRetry`), а контракт — общее для фейка и pgstore: не больше одного живого токена.
- Чужие файлы: строка authnkit в корневых `README.md` (§ Пакеты) и `SECURITY.md` (§ Что в скоупе) — описание своего кита.

## Handoff
### Аннотация `v1.6.0` — authnkit
Breaking:
- authnkit: `Hasher.VerifyDummy` и `(*Argon2idHasher).VerifyDummy`: `(ctx)` → `(ctx) error` — ошибку обработать как
  ошибку `Verify` (ответ тот же); своя реализация `Hasher` — новая сигнатура.
- authnkit: `Hash`, `Verify`, `VerifyDummy` при полной очереди Argon2id — `ErrBusy` сразу (было: ожидание слота до
  дедлайна ctx) → правило `errors.Is(err, authnkit.ErrBusy)` → 503 на всех путях пароля (вход по известному и
  неизвестному адресу, регистрация, смена и сброс), `Retry-After` — из `RetryAfter()` ошибки в цепочке; без правила — 500.
- testkit/authntest: `DeterministicHasher.VerifyDummy` отдаёт `error`.
- authnkit/session/pgstore: миграция `0002` источника `authnkit_session` (`successor_hash` в `authn_session_tokens`) —
  до старта новой версии: без неё старт — `pgkit.ErrSchema`; `pgkit.MigrateModule` в графе применяет её сам.
- testkit/sessiontest: `RunContract` проверяет окно повтора — своя реализация `session.Store` должна его держать.

Поведение:
- authnkit/session: `SESSION_RETRY_WINDOW` = 30s — повтор использованного refresh-токена в окне после его ротации,
  пока преемник не использован, выдаёт нового преемника и гасит прежнего, а не отзывает сессию; `0` — прежняя строгость.
- authnkit/session/pgstore: `authn_session_rotations_total{result="retry"}` — честный повтор, `reused` его не считает.
- authnkit: гистограммы `authn_password_*_duration_seconds` пишут только допущенные в очередь вычисления.

Изменения:
- authnkit: `ErrBusy` (`RetryAfter()` — 1 с), метрика `authn_password_busy_total`.
- authnkit/session: `Config.RetryWindow` (`SESSION_RETRY_WINDOW`).
- testkit/authntest: `DeterministicHasher.Busy` — `ErrBusy` у `Hash`, `Verify` и `VerifyDummy`.

### Потребителю — шаблон (трек g-upgrade)
1. `go get …/golibinfra@v1.6.0`; миграция `0002` кита идёт командой миграций (`cmd/migrate/sources`), без эффекта
   (`ADD COLUMN` без значения по умолчанию).
2. `auth` — вместе с bump, иначе под нагрузкой известный адрес отвечает 500, а неизвестный 401 сразу: ошибка
   `VerifyDummy` — как ошибка `Verify`; `authnkit.ErrBusy` на путях пароля (`Login`, `ChangePassword`, `user.Register`,
   сброс пароля `verification`) → ошибка домена с 503 и `Retry-After` (ошибка кита — в цепочке); правило в таблицах
   операций, `make gen` (`openapi.json`); тесты — `authntest.DeterministicHasher{Busy: true}` на известном и неизвестном
   адресе. Guard входа (`LoginGuard.Attempt` до проверки пароля) считает отказ `ErrBusy` неудачей: клиент, повторяющий
   отказ, блокирует свою пару «адрес — IP» (H2: 429 на десятой попытке) — решение шаблона (у `bruteforcekit.Guard`
   возврата попытки нет; `RecordSuccess` на отказе снял бы защиту от перебора).
3. Повтор refresh: `TestSessions` (`cmd/api`) ждёт 401 на немедленный повтор и одного победителя параллельных обновлений,
   smoke `test/smoke/test_auth.py` — 401 на повтор погашенного cookie: на окне по умолчанию это повтор в окне (204, прежний
   преемник погашен). Кейсы — без окна (`SESSION_RETRY_WINDOW=0`) или повтор после использования преемника, плюс кейс
   окна; `usecase_test.go` (`auth`) — литерал `session.Config` без окна, не меняется.
4. Описание операции `refreshToken`, `SECURITY.md` (лимит входа), `docs/stack/_templates/auth.md`,
   `docs/reference/architecture.md` (повтор refresh в окне — не кража; алерт `reused` на одиночный повтор не срабатывает),
   `.env.example` (`SESSION_RETRY_WINDOW`), якоря `scripts/tools/mutants.txt` у правленого кода.

## Где мы сейчас
Все шаги и критерии закрыты: хэшер отказывает при полной очереди (`ErrBusy`), повтор ротации в окне выдаёт нового
преемника, эксперименты аудитора на копии шаблона чисты, «Handoff» сверен с `release-diff`. Дальше — валидатор трека,
затем дирижёр: слияние в `main`, удаление плана, тег `v1.6.0` с аннотацией из «Handoff» и трек g-upgrade шаблона.
