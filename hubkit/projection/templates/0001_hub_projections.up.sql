-- Локальные read-проекции хаба (00-architecture §2, §4). Наполняются только
-- через hubkit/sync; write-through идёт в hub API, не сюда. id — UUIDv7 хаба,
-- глобальные для всей экосистемы (без перекодировок). updated_seq — курсор фида.

CREATE TABLE hub_companies (
    id            UUID         PRIMARY KEY,
    name          TEXT         NOT NULL,
    slug          TEXT         NOT NULL,
    legal_name    TEXT         NOT NULL DEFAULT '',
    inn           TEXT         NOT NULL DEFAULT '',
    kpp           TEXT         NOT NULL DEFAULT '',
    ogrn          TEXT         NOT NULL DEFAULT '',
    legal_address TEXT         NOT NULL DEFAULT '',
    bank_details  JSONB        NOT NULL DEFAULT '{}'::jsonb,
    contacts      JSONB        NOT NULL DEFAULT '{}'::jsonb,
    links         JSONB        NOT NULL DEFAULT '{}'::jsonb,
    archived      BOOLEAN      NOT NULL DEFAULT FALSE,
    version       BIGINT       NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ  NOT NULL,
    updated_at    TIMESTAMPTZ  NOT NULL,
    updated_seq   BIGINT       NOT NULL
);
CREATE INDEX hub_companies_slug_idx ON hub_companies (slug);
CREATE INDEX hub_companies_inn_idx  ON hub_companies (inn) WHERE inn <> '';

CREATE TABLE hub_projects (
    id          UUID         PRIMARY KEY,
    company_id  UUID         NOT NULL REFERENCES hub_companies (id),
    name        TEXT         NOT NULL,
    slug        TEXT         NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    archived    BOOLEAN      NOT NULL DEFAULT FALSE,
    position    BIGINT       NOT NULL DEFAULT 0,
    version     BIGINT       NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL,
    updated_at  TIMESTAMPTZ  NOT NULL,
    updated_seq BIGINT       NOT NULL
);
CREATE INDEX hub_projects_company_idx ON hub_projects (company_id);

CREATE TABLE hub_employees (
    id          UUID         PRIMARY KEY,
    subject     TEXT         NOT NULL UNIQUE,
    username    TEXT         NOT NULL,
    full_name   TEXT         NOT NULL DEFAULT '',
    email       TEXT         NOT NULL DEFAULT '',
    department  TEXT         NOT NULL DEFAULT '',
    role        TEXT         NOT NULL DEFAULT '',
    is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    version     BIGINT       NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ  NOT NULL,
    updated_at  TIMESTAMPTZ  NOT NULL,
    updated_seq BIGINT       NOT NULL
);
CREATE INDEX hub_employees_username_idx ON hub_employees (username);

-- Per-service видимость: продукт фильтрует свои списки по hidden своего service.
CREATE TABLE hub_entity_visibility (
    service     TEXT         NOT NULL,
    entity_kind TEXT         NOT NULL,
    entity_id   UUID         NOT NULL,
    hidden      BOOLEAN      NOT NULL DEFAULT FALSE,
    updated_seq BIGINT       NOT NULL,
    PRIMARY KEY (service, entity_kind, entity_id)
);
CREATE INDEX hub_entity_visibility_entity_idx ON hub_entity_visibility (entity_kind, entity_id);

-- Курсоры pull-синка (hubkit/sync.CursorStore): по строке на ресурс.
CREATE TABLE hub_sync_cursors (
    resource   TEXT         PRIMARY KEY,
    cursor     BIGINT       NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
