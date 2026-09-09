# hubkit — архитектура

Хаб — источник истины экосистемы stit: компании, проекты, сотрудники, теги,
департаменты, видимость сущностей и обе identity-сессии. Продукт не проксирует
хаб на горячем пути: сессию он проверяет локально по JWKS, а справочные данные
держит в собственной read-проекции, которую наполняет pull-синком.

```
      employee-backend            company-backend             tag-backend
      (id.stit.tech)              (api.hub.stit.tech)         (tags.hub.stit.tech)
        │  hubkit/issuer            │  hubkit/issuer            │
        │  JWKS + сессия сотрудника │  JWKS + сессия заказчика  │
        ▼                           ▼                           ▼
   ┌──────────────────────────── hub API + sync-фиды ────────────────────────────┐
   │                                                                             │
   ▼                                                                             ▼
 продукт: hubkit/sso (cookie → Identity)         продукт: hubkit/client + hubkit/sync
                                                  → локальная проекция (hubkit/projection)
```

## Два контура identity

| | Сотрудник | Заказчик |
|---|---|---|
| `Identity.Kind` | `employee` (пустой claim нормализуется в него) | `customer` |
| Issuer / `iss` | employee-backend, `https://id.stit.tech` | company-backend, `https://api.hub.stit.tech` |
| JWKS | `HUB_SSO_JWKS_URL` | `HUB_SSO_CUSTOMER_JWKS_URL` |
| `aud` | `HUB_SSO_AUDIENCE` | `HUB_SSO_CUSTOMER_AUDIENCE` |
| Cookie | `stit_session` | `stit_customer` |
| Заполненные поля | `EmployeeID`, `Username`, `Role` | `CompanyID`, `AccountRole`, `Products` |
| Вход | Authentik (OIDC BFF) | логин + пароль на `hub.stit.tech/customer/login` |

Общее: cookie `Domain=.stit.tech`, HttpOnly, Secure, SameSite=Lax, access-TTL
15 минут, подпись ES256.

Барьеров между контурами три, и каждый обязателен: `iss`, `aud` и claim `kind`.
`HUB_SSO_ISSUER` и `HUB_SSO_AUDIENCE` — обязательные переменные именно поэтому:
опечатка в compose не должна снимать барьер. Customer-токен без `company_id`
отвергается (`ErrNoCompany`): видимость заказчика определяется его компанией, а
пустое значение читалось бы продуктом как «любая».

**Приоритет — за сотрудником.** Слоты независимы: сотрудник и заказчик могут
оказаться в одном браузере, и общая cookie молча выбивала бы один вход другим.
При обеих заполненных выигрывает `stit_session`; протухший employee-токен не
отменяет валидную сессию заказчика.

**Нулевая `Identity` не принципал.** `IsEmployee()`/`IsCustomer()` требуют
непустой `Subject`, поэтому литерал `Identity{}` в ctx не проходит
`RequireEmployee` (`ErrNoIdentity`) и не даёт `CanUseProduct` = true.

## Проверка токена

Verifier слота разбирает JWS с allowlist'ом `alg` из одного элемента (ES256):
продукт держит только публичный ключ, симметричную подпись мог бы подделать
любой держатель секрета, а `alg=none` не даёт подписи вовсе. Дальше — ключ по
`kid` из JWKS-кэша, подпись, `iss`/`aud`/`exp`/`nbf`/`iat` с допуском
`HUB_SSO_LEEWAY`, обязательный `exp`, обязательный `sub`, `kind` и (у заказчика)
`company_id`.

JWKS-кэш (`internal/jwks`, общий с `oidckit`): ленивая загрузка (старт
приложения не блокируется), догрузка по неизвестному `kid` и перечитывание по
истечении окна свежести, параллельные промахи сливаются в одну загрузку, у
загрузки собственный дедлайн и detached ctx — отмена одного запроса не роняет
общий refresh. Окно свежести `hubkit/sso` берёт из `Cache-Control: max-age`
ответа JWKS (то есть из `HUB_SESSION_JWKS_MAX_AGE` issuer'а), не меньше минуты
и не больше 15 минут; перечитывание запускает следующий `Verify` после
истечения окна, отдельной горутины кит не держит. Неудачное перечитывание не
отзывает уже известный ключ: простой JWKS не должен ронять проверку живых
токенов. Недоступность JWKS — отдельная ошибка `ErrJWKSUnavailable`;
`Extractor` оборачивает её в `httpkit.ErrAuthUnavailable`, и клиент получает
503, а не 401.

## Ротация ключа подписи

У issuer'а три слота: `previous` (только публичный ключ), `active` (подписывает)
и `next` (только публичный). Все опубликованные слоты попадают в JWKS.

1. Сгенерировать новый ключ и выкатить его публичный PEM в next-слот
   (`HUB_SESSION_NEXT_PRIVATE_KEY` принимает и публичный PEM — приватный ключ
   следующего `kid` инстансу не нужен). Верификаторы узнают новый `kid` заранее.
2. Дождаться, пока набор разойдётся по продуктам (не меньше
   `HUB_SESSION_JWKS_MAX_AGE`), затем сделать новый ключ активным, а прежний
   активный перенести в `HUB_SESSION_PREVIOUS_PUBLIC_KEY`.
3. Через `HUB_SESSION_TTL` после переключения previous-слот можно снять: токенов,
   подписанных им, больше нет.

На стороне продукта тот же интервал страхует `HUB_SSO_KEY_GRACE`: `kid`,
выведенный из набора, продолжает проверять подписи ещё это время после
перечитывания JWKS. Без обоих механизмов шаг «active = next» отдавал бы 401 всем
живым access-токенам.

Отзыв скомпрометированного ключа — та же процедура без ожидания: убрать ключ из
слотов и перезапустить issuer. Продукт перестанет доверять ему через
`HUB_SESSION_JWKS_MAX_AGE` (перечитывание набора) + `HUB_SSO_KEY_GRACE`
(grace предыдущего поколения). Уже выпущенные этим ключом access-токены живут
до своего `exp` в любом случае, поэтому полное вытеснение утёкшей сессии — это
`RefreshManager.Revoke(sid)` плюс истечение access-TTL, а не ротация ключа.
Нужно быстрее — выкатить продукты с `HUB_SSO_KEY_GRACE=0`.

## Rotating refresh

Состояние живёт в `cachekit.Cache` (Redis в проде, `MemoryCache` — для
single-replica). В кэше только `sha256` токенов; сам токен — 256 бит из
`crypto/rand` и наружу уходит только в cookie.

Обмен (`Rotate`):

1. `SetNX(used:<hash>)` — резерв обмена. Резерв ставится **до** гашения записи:
   проигравший в гонке всегда видит детерминированный маркер, а не «токена нет».
2. `GetDel(refresh:<hash>)` — атомарное гашение старого токена. Промах →
   резерв снимается `CompareAndDelete` (чтобы произвольная строка клиента не
   оседала в кэше) и `ErrRefreshInvalid`.
3. Проверка tombstone по `sid` → `ErrRefreshRevoked`.
4. Маркер перезаписывается связкой `{sid, at}`, выдаётся новый токен.

Исходы повторного предъявления одного токена:

| Ситуация | Результат |
|---|---|
| Повтор внутри `HUB_REFRESH_REUSE_GRACE` | `ErrRefreshInFlight`, семейство **не** отзывается: это параллельный обмен (две вкладки, ретрай после обрыва), и клиент уже держит `Set-Cookie` победителя |
| Повтор позже grace | `Revoke(sid)` + `ErrRefreshReused`: признак утечки, вся цепочка мертва |
| Обмен не дошёл до выдачи (маркер без `sid`) | `ErrRefreshInvalid`: живого токена этой цепочки нет, отзывать нечего |
| Логаут (`Revoke`) | любой токен цепочки → `ErrRefreshRevoked` до истечения `HUB_REFRESH_TOMBSTONE_TTL` |

Grace-окно закрывает гонку, но не потерянный ответ: если клиент не получил
`Set-Cookie` победителя (обрыв на пути назад), нового токена у него нет.
Повтор внутри окна даст `ErrRefreshInFlight` (409 по рецепту
[usage.md](usage.md)), позже — `ErrRefreshReused` и отзыв семейства, то есть
разлогин. Осознанный размен: хранить выданный токен на время grace и отдавать
его повторно значило бы держать в кэше живой refresh-токен в открытом виде.
Клиент лечит это ретраем внутри grace-окна, а не после него.

Любая ошибка кэша — отказ обмена (fail-closed): недоступный Redis не должен
превращаться в бесконечно валидный refresh. Отзыв семейства при детекте утечки
и снятие резерва идут на ctx, отвязанном от запроса (собственный дедлайн 5s):
обрыв соединения не должен подавлять реакцию на утечку.

Плата за reuse-detection — маркер `hub:refresh:used:<sha256>` на каждый обмен,
живущий `HUB_REFRESH_TTL`; сайзинг Redis — в
[configuration.md](configuration.md).

`Session` — нейтральная структура обоих контуров (`Kind`, `CompanyID`,
`AccountRole`, `Products` рядом с полями сотрудника); `Session.Claims()` собирает
из неё карту claim'ов по константам `hubkit/hubclaims`, а `Signer.Sign` добавляет
`iss`/`aud`/`exp`/`iat`/`nbf`. Claims caller'а не могут перекрыть ни один из них
(`ErrReservedClaim`), а `sub` обязан быть непустой строкой (`ErrNoSubject`):
одно лишнее поле карты иначе выпустило бы токен на чужой `aud` или без срока.
`kind=customer` без `company_id` не выпускается (`ErrNoCompany`) ни через
`Session` (`Issue`), ни напрямую (`Sign`) — зеркало барьера verify-стороны:
такой токен всё равно отвергнет каждый продукт, и диагностика всплыла бы у
пользователя, а не на стороне выпуска.

## Клиент hub API

Один `*client.Client` роутит запросы по трём backend'ам: company
(`HUB_BASE_URL`), employee (`HUB_EMPLOYEE_BASE_URL`) и tag (`HUB_TAG_BASE_URL`);
незаданные падают на `HUB_BASE_URL`. Аутентификация — статический Bearer
(`HUB_SERVICE_TOKEN`), поверх `httpclient` (retry, breaker на хост, OTel,
`X-Request-ID`). Каждый запрос идёт с дедлайном: ctx без него получает 30 секунд
на все попытки.

Ошибки типизированы: `ErrNotFound`, `ErrConflict`, `ErrUnauthorized` и
`ErrUnavailable`. Последняя — сигнал «деградируй на локальную проекцию», а не
бизнес-ошибка; цепочка сохраняется целиком, поэтому под ней различимы
`context.DeadlineExceeded` и `httpclient.ErrBreakerOpen`.

### Retry-safety

`httpclient` сам повторяет только GET/HEAD/OPTIONS/PUT/DELETE/TRACE. Методы,
идемпотентные по контракту хаба, помечают запрос явно
(`httpclient.AllowRetry`):

| Метод | Повтор | Почему |
|---|---|---|
| Все `Get*`/`List*`, `SyncFeed` | да | чтение |
| `ReplaceEntityTags`, `ReplaceEmployeeDepartments`, `SetVisibility(Entry)` | да | PUT с replace-семантикой |
| `UpsertTag`, `UpsertDepartment` | да (`AllowRetry`) | POST, но хаб дедуплицирует по `norm_name` — повтор возвращает ту же запись |
| `ArchiveCompany`/`UnarchiveCompany`/`ArchiveProject`/`UnarchiveProject` | да (`AllowRetry`) | POST, выставляющий состояние |
| `CreateCompany`, `CreateProject` | **нет** | не идемпотентны, а `Idempotency-Key` хаб не поддерживает: повтор дал бы вторую сущность (в лучшем случае `ErrConflict` по slug) |
| `UpdateCompany`, `UpdateProject` | нет | PATCH; для защиты от гонки — `Version` (оптимистичная блокировка) |

Потребителю, включившему `HTTPCLIENT_RETRY_NON_IDEMPOTENT`, стоит помнить, что
create'ы хаба в этом режиме могут задвоиться.

## Sync-движок

Один `Engine` на ресурс. Тик (`Run`) догоняет фид до конца: читает страницами
`after=<cursor>&limit=<HUB_SYNC_LIMIT>`, отдаёт батч сырых `json.RawMessage` в
`ProjectionStore.Upsert` продукта и двигает курсор через `CursorStore`. Курсор
монотонен, апсерт идемпотентен по id — повторная доставка и catch-up после
простоя безопасны. Страница короче лимита завершает тик; батч, не продвинувший
курсор, обрывает цикл с предупреждением (защита от зацикливания).

Движок generic по `json.RawMessage`: тип элемента знает продукт. `entity-tags` и
`department_ids` сотрудника — snapshot-ресурсы: элемент несёт полный набор на
курсоре, и продукт применяет его как replace, а не как дельту.

Расписание — `timerkit.Job` (`Interval`, `Timeout`, `Jitter`, `RunOnStart` из
`Config`), поэтому запуски не перекрываются и глушатся на graceful shutdown.
Отмена ctx посреди тика ошибкой не считается.

## Projection — эталон схемы, а не рантайм

`templates/*.sql` (миграции `hub_companies` / `hub_projects` / `hub_employees` /
`hub_entity_visibility` + `hub_sync_cursors`) и `queries/*.sql` (sqlc-эталон:
идемпотентный апсерт из фида + листинги) доступны как `embed.FS`
(`MigrationsFS`, `QueriesFS`) и как готовый источник миграций
`projection.Migrations` (`pgkit.Source`, таблица версий
`schema_migrations_hub_projection`).

Известные расхождения шаблона с моделями `hubkit/client` — продукт обязан их
учитывать:

- **`hub_employees.department TEXT` недостижим из фида.** `client.Employee` не
  имеет строкового поля `department`: принадлежность сотрудника выражается
  массивом `department_ids` (snapshot M2M). Колонка шаблона остаётся пустой;
  продукту, которому нужны департаменты, требуется собственная колонка-массив
  или таблица связи.
- **Нет таблиц `hub_departments`, `hub_tags`, `hub_entity_tags`.** Проекции
  ресурсов `departments`, `tags` и `entity-tags` продукт описывает сам по типам
  `client.Department`, `client.Tag` и `client.EntityTagsSnapshot`.
- **`hub_projects.company_id` — FK на `hub_companies`.** Ресурсы `companies` и
  `projects` синкаются независимыми тиками без упорядочивания: проект, приехавший
  раньше своей компании, валит тик `projects` FK-ошибкой до следующего тика
  `companies`. Продукту, которому такая пауза неприемлема, FK нужно снять.
- **`GetSyncCursor` не возвращает строки для нового ресурса.** Реализация
  `CursorStore.Load` обязана маппить `ErrNoRows` в `0`, иначе первый тик падает
  вместо старта фида с начала.
