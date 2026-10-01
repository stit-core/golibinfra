# Backlog

Кандидаты в работу, ещё не взятые в план: `- **<id>** — что и зачем. Триггер: когда браться.` Форма и жизненный цикл — skill `/plan`.

- **archkit** — общие правила раскладки `internal/domain/<d>` из archtest шаблона — китом с одной точкой
  входа, allowlist остаётся у форка (replan §5–6). Триггер: партия 3, после `fix-reference-code`.
- **env-reader** — один ридер ENV в `internal/`: пустое = не задано, fallback префикса только явный,
  `ErrConfig` на ошибке разбора; breaker httpclient с префиксом (KAPI-02, KAPI-04, KDOC-03). Триггер:
  партия 3.
- **config-validate** — конструкторы зовут `Validate` (outboxkit `New`/`NewWorker`, `storagekit.Module`,
  `httpkit.NewServer` с ошибкой), одна семантика `Validate`/нулей/nil-опций, кросс-инвариант
  outbox/pgkit на старте (KAPI-01, KAPI-05, KAPI-08). Триггер: партия 3.
- **api-forms** — одна форма групп, хелперов регистрации, проб и логгера; группа `httpclient.options` —
  упорядоченным значением; test-хелперы из китов или исключения в правиле; место в ошибке графа
  httpkit (KAPI-06, KAPI-07, KTST-07, KFX-01, KFX-10). Триггер: партия 3.
- **url-secrets** — общий `redactURL` в `internal/` для URL и DSN в ошибках, логах и спанах; спаны и
  ошибки httpclient без query (KDOC-02, KSEC-03). Триггер: партия 3.
- **outbox-order-trace** — `ordered` по моменту вставки; trace-context события в строке outbox;
  label `subject` и имя producer-спана — шаблоном, не сырым subject (KCOR-02, KOBS-02, KOBS-03,
  KOBS-04). Триггер: партия 3.
- **pg-lifecycle** — мигратор с `lock_timeout`/`StatementTimeout`/`GracefulStop`; `OnStop` пула по
  ctx; вложенный `WithTxOptions` строже внешнего — ошибка; проверка схемы китов на старте — хелпер
  из `authnkit/session` (KCOR-03, KFX-04, KCOR-07, KFX-02). Триггер: партия 3.
- **cachekit-atomic** — `GetDel`/`SetNX` без ретраев, `Set` резерва по токену владельца (breaking
  интерфейса httpkit), контрактный набор `IdempotencyStore`, предел `MemoryCache` и breaker'ов
  (KCOR-04, KCOR-05, KTST-10, KSEC-06). Триггер: партия 3.
- **httpkit-contract** — 413 для лимита тела операции, идемпотентность без 408/425/429,
  `WWW-Authenticate` и HEAD, scope анонимов, дедлайн запроса, CORS `Idempotency-Key`, panic после
  заголовка, `BindAPI` под мьютексом (KCOR-09…-12, KSEC-13…-15, KAPI-09). Триггер: партия 3.
- **httpkit-auth-edge** — extractor видит схему операции; `traceparent`/`baggage` клиента на краю не
  принимаются; один вызов extractor'а на запрос (KSEC-08, KSEC-12, KFX-11). Триггер: партия 3.
- **httpkit-signals-gate** — тест контракта сигналов httpkit в гейте (6 метрик, labels, unit,
  access-log), `unmatched` только для неизвестных маршрутов, негативные тесты `Idempotency` (hk-ctr-05)
  и хелперов handlertest, конкурентные тесты лимитера (KGATE-13, KTST-11, KOBS-12). Триггер: партия 3.
- **fakes-parity** — фейки как боевые: natstest валидирует subject/стрим/размер, outboxtest и
  audittest маршалят и видят откат (`pgtest` с откатом), logtest через `slog.Record`, authntest,
  `MemoryCache`, таймеры clockkit (KTST-03, KTST-04, KTST-05, KDOC-08). Триггер: партия 3.
- **tests-mutants** — убить 7 выживших мутантов (jwks ×4, `RecordSuccess` bruteforcekit, `OnStop`
  cachekit, `Reset` clockkit) и тест таймаута healthkit с дедлайном (KTST-02). Триггер: партия 3.
- **gate-rules** — forbidigo/contextcheck и запрет `:latest` в гейте, пути гейта в «Сначала
  спросить» и сверка набора линтеров, commit-msg и apidiff в `/release`, версии тулов (KGATE-07…-10).
  Триггер: партия 3.
- **fx-startup** — ленивые модули с `fx.Invoke`, экспорт telemetry на остановке best-effort,
  `FxLogger` без OTel-sink, логгер go-redis через logkit (KFX-03, KFX-05, KFX-06, KOBS-08). Триггер:
  партия 3.
- **obs-conventions** — semconv БД одной версии, ключ ошибки `error` во всех китах, `not_found` в
  storagekit, TOTP-метрика — одна точка на вызов (KOBS-06, KOBS-07, KOBS-09, KDOC-05). Триггер:
  партия 3.
- **security-defaults** — бюджет перебора на subject по всем адресам, `CLEANUP_RETENTION` ≥
  `LOCK_DURATION`, TLS S3 и `OTEL_TOKEN` не по `http://`, предел длины пароля Argon2id (KSEC-04,
  KSEC-07, KSEC-05, C). Триггер: партия 3.
- **auditkit-input** — `Validate` отвергает NUL и не-UTF-8, предел `Metadata`, retention `audit_log`
  (KCOR-06, C). Триггер: партия 3.
- **docs-accuracy** — тексты к коду: остаток KDOC-07, числа KDOC-09 вне outbox, советы KDOC-10,
  примеры auth httpkit KDOC-11. Триггер: партия 3.
- **logkit-redact-gaps** — маскирование пропускает двусмысленные короткие ключи (`pass`, `sid`,
  `code`, `signature`, `refresh`), поля структур в `Any`, base64url-токены, пароль в DSN, коды вне
  словаря (`passcode`, `verification_code`) и прячет счётчики рядом с кодом (`otp_attempts`)
  (`M/val-kits-A/experiments/logkit_zz_val_exp_test.go`). Триггер: партия 3.
- **logkit-redact-containers** — ключи-секреты не ищутся в `map` под указателем (безопасно —
  указатель считать контейнером, только если его цель — контейнер) и в JSON из `json.RawMessage`
  и `[]byte` (тело запроса `{"password":…}`): их видят только маски. Триггер: партия 3.
- **logkit-redact-otpauth** — ключ `otpauth` (URI provisioning TOTP) по правилу ключей не секрет, а
  base32-секрет в `secret=` дефолтные маски не ловят — он уходит в лог открытым. Триггер: партия 3.
- **bruteforcekit-lock-remaining** — `Retry-After` отказа потребитель считает своими часами от
  `LockState.Until` (часы БД): расхождение часов даёт неверную паузу; отдавать остаток блокировки
  от хранилища. Триггер: партия 3.
- **bruteforcekit-attempt-retry-test** — ветку `Attempt`, которая переучитывает попытку, когда
  блокировку сняли между чтением и upsert (до 3 раз), тест не воспроизводит. Триггер: партия 3.
- **outboxtest-withtesttx** — `pgkit.WithTestTx` на ctx usecase'а снова пропускает `Publish` мимо
  транзакции: у фейков `pgtest` и ручной пометки один маркер; контракт держат
  `outboxkit/docs/testing.md` и аннотация v1.2.0, машина — нет. Триггер: партия 3 (fakes-parity).
- **timerkit-raw-group** — job с сырым тегом группы `timerkit.jobs` обходит маркер модуля (проверку
  `timerkit.Module` в графе); контракт «только `timerkit.Provide`» держит README. Триггер: партия 3
  (`archkit`).
