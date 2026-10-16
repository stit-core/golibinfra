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
- [ ] **AC1** — RSECT-01: при полной очереди Argon2id `Hash`, `Verify` и `VerifyDummy` отказывают сразу ошибкой
  `ErrBusy` с `RetryAfter()`, а не ждут слот до дедлайна запроса; проверка под потоком сильнее ёмкости хэшера ждёт не
  дольше очереди (девять хэшей на слот) или получает отказ сразу. Проверка: `go test -race -run 'TestArgon2id_FullQueue' ./authnkit/` → PASS; проба `zz_gauth_hash_probe_test.go` при `GOMAXPROCS=4` — база: секунды до дедлайна, ветка: не дольше 0.5 с или `ErrBusy` сразу
- [ ] **AC2** — отказ при полной очереди не выдаёт учётную запись: путь «учётной записи нет» (`VerifyDummy`) и пароль
  длиннее предела получают тот же `ErrBusy`, что `Verify`; фейк `authntest` даёт этот исход тестам потребителя.
  Проверка: `go test -race -run 'TestArgon2id_FullQueue|TestDeterministicHasher' ./authnkit/ ./testkit/authntest/` → PASS
- [ ] **AC3** — RCOD-16: повтор `Rotate` тем же токеном не позже `SESSION_RETRY_WINDOW` (по умолчанию 30 с) после его
  ротации, пока преемник не использован, выдаёт нового преемника и гасит прежнего — сессия жива; повтор вне окна, после
  использования преемника и предъявление погашенного преемника — `*ReusedError` и отзыв сессии, как прежде. Проверка: `sessiontest.RunContract` на фейке и pgstore → PASS; проба `zz_gauth_session_probe_test.go` — база: повтор через 2 с — `ErrReused`, ветка: новый токен, три сценария кражи — `ErrReused`
- [ ] **AC4** — параллельные `Rotate` одним токеном в окне не оставляют двух живых токенов сессии. Проверка: `go test -race -run 'TestStore_ConcurrentRetry' ./authnkit/session/pgstore/` → PASS
- [ ] **AC5** — эксперименты аудитора на копии шаблона: H2 (лимит `/api/auth/*` снят) — вход жертвы на базе секунды,
  на ветке не дольше очереди или отказ сразу; E3 — на базе `AUTH_REFRESH_REUSED` и закрытая сессия, на ветке — 200,
  сессия открыта, записи `refresh_reused` нет. Проверка: логи `N/g-auth/tpl-*.log`
- [ ] **AC6** — доки и сигналы кита по факту: README и `docs/` authnkit, строки testkit и корневого README, `ErrBusy` и
  ответ на него (503 и `Retry-After`), `authn_password_busy_total`, `result="retry"`, `SESSION_RETRY_WINDOW`; «Handoff» —
  строки аннотации `v1.6.0` и шаги шаблона, сверенные с `make release-diff`. Проверка: `make envdoc-check` → OK; `make release-diff PREV=v1.5.0` — каждая строка в «Handoff»

## Шаги
- [ ] **S1** — authnkit: очередь хэшера с отказом `ErrBusy` (RSECT-01) — вычисления ждут слот не дальше
  `queuePerSlot` на слот, лишние — `ErrBusy` сразу; `VerifyDummy` отдаёт ошибку; `authn_password_busy_total`; фейк
  `authntest` (`VerifyDummy` с ошибкой, поле `Busy`); доки кита и testkit
  - Проверка: `go test -short -race ./authnkit/ ./testkit/authntest/` → PASS, регрессия на базе красная; проба H на ветке; `make check` → `✓ check clean (./...)` · Закрывает: AC1, AC2, AC6
- [ ] **S2** — authnkit/session: окно повтора ротации (RCOD-16) — `Config.RetryWindow` (`SESSION_RETRY_WINDOW`),
  миграция `0002` (`successor_hash`), pgstore и фейк, кейсы `RunContract`, `result="retry"`, доки
  - Проверка: `go test -race ./authnkit/session/... ./testkit/sessiontest/` → PASS, регрессия на базе красная; проба R на ветке; `make check` → `✓ check clean (./...)` · Закрывает: AC3, AC4, AC6
- [ ] **S3** — эксперименты аудитора H2 и E3 на копии шаблона (`git archive`): база `v1.5.0` и ветка через `replace`
  - Проверка: логи `N/g-auth/tpl-*.log` · Закрывает: AC1, AC3, AC5 · Зависит от: S1, S2
- [ ] **S4** — «Handoff» сверен с `make release-diff PREV=v1.5.0`; гейт на чистом дереве
  - Проверка: `make gate-clean` → `✓ check clean (./...)` · Закрывает: AC6 · Зависит от: S3

## Решения
- RSECT-01 — отказ по длине очереди, а не по сроку ожидания: при полной очереди отказ приходит сразу и освобождает
  соединение и горутину, а ожидание по таймеру держало бы каждый лишний запрос весь срок; длина очереди на слот
  ограничивает ожидание принятого вызова (восемь хэшей впереди) при любом числе ядер.
- Глубина очереди — константа кита (8 на слот), без переменной: число слотов кит тоже выводит сам
  (`GOMAXPROCS/ARGON2_THREADS`); поле в `Argon2idConfig` смешало бы окно параметров PHC (`NeedsRehash` сравнивает
  структуру) с очередью, а ноль в литерале конфига у потребителей молча значил бы «без очереди». Настройка — когда
  потребителю её не хватит.
- Ответ на `ErrBusy` — 503 и `Retry-After`, а не 429: полная очередь — перегрузка процесса для всех клиентов, а не
  лимит клиента. Ошибка несёт `RetryAfter()` (1 с), и `httpkit.ErrorMap` ставит заголовок сам; `ErrBusy` — значение
  своего типа с этим методом, поэтому фейк `authntest` отдаёт ту же ошибку, что боевой хэшер.
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
  `reused` не срабатывает на одиночный повтор клиента, параллельное обновление по-прежнему видно.
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

## Handoff
### Аннотация `v1.6.0` — authnkit
Breaking:
- authnkit: `Hasher.VerifyDummy(ctx)` → `VerifyDummy(ctx) error` — ошибку обработать как ошибку `Verify` (ответ тот же);
  своя реализация `Hasher` — новая сигнатура.
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
2. `auth`: ошибка `VerifyDummy` — как ошибка `Verify`; `authnkit.ErrBusy` на путях пароля (`Login`, `ChangePassword`,
   `user.Register`, сброс пароля `verification`) → ошибка домена с 503 и `Retry-After` (ошибка кита — в цепочке);
   правило в таблицах операций, `make gen` (`openapi.json`); тесты — `authntest.DeterministicHasher{Busy: true}` на
   известном и неизвестном адресе.
3. Описание операции `refreshToken`, `SECURITY.md` (лимит входа), `docs/stack/_templates/auth.md`,
   `docs/reference/architecture.md` (повтор refresh в окне — не кража; алерт `reused` на одиночный повтор не срабатывает),
   `.env.example` (`SESSION_RETRY_WINDOW`), якоря `scripts/tools/mutants.txt` у правленого кода.

## Где мы сейчас
План заведён; пробы на базе воспроизводят оба дефекта (`N/g-auth/probe-*-base.log`). Следующий шаг — S1: очередь
хэшера с отказом `ErrBusy`.
