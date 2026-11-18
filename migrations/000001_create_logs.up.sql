BEGIN;

CREATE TABLE logs (
    id          uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    account_id  text NOT NULL,
    message     text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX logs_account_created_idx
    ON logs (account_id, created_at DESC);

CREATE TABLE api_keys (
    id              uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    account_id      text NOT NULL,
    key             text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    exp             bigint NOT NULL,
    rate_limit_window bigint NOT NULL,
    rate_limit_max  bigint NOT NULL,
    soft_deleted    boolean NOT NULL DEFAULT FALSE,
    deactivated_at  timestamptz,
    deactivated_by  text,
    scopes          text[] NOT NULL DEFAULT '{}'
);

CREATE INDEX api_keys_account_created_idx
    ON api_keys (account_id, created_at DESC);

CREATE INDEX logs_account_created_id_idx
ON logs (account_id, created_at DESC, id DESC);

CREATE TABLE accounts (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username              text NOT NULL,
    email                 text NOT NULL,

    password_hash         text NOT NULL,
    password_changed_at   timestamptz NOT NULL DEFAULT now(),

    role                  text NOT NULL DEFAULT 'member',
    status                text NOT NULL DEFAULT 'pending_verification',

    email_verified_at     timestamptz,

    mfa_secret_encrypted  bytea,
    mfa_enabled_at        timestamptz,

    failed_login_count    integer NOT NULL DEFAULT 0,
    locked_until          timestamptz,
    last_login_at         timestamptz,

    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    deleted_at            timestamptz,

    CONSTRAINT accounts_username_length
        CHECK (char_length(username) BETWEEN 3 AND 64),
    CONSTRAINT accounts_username_trimmed
        CHECK (username = btrim(username)),
    CONSTRAINT accounts_email_length
        CHECK (char_length(email) BETWEEN 3 AND 254),
    CONSTRAINT accounts_role_valid
        CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    CONSTRAINT accounts_status_valid
        CHECK (status IN ('pending_verification', 'active', 'suspended')),
    CONSTRAINT accounts_failed_login_count_nonnegative
        CHECK (failed_login_count >= 0),
    CONSTRAINT accounts_mfa_consistent
        CHECK ((mfa_enabled_at IS NULL) OR (mfa_secret_encrypted IS NOT NULL))
);

CREATE UNIQUE INDEX accounts_username_unique
    ON accounts (lower(username)) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX accounts_email_unique
    ON accounts (lower(email)) WHERE deleted_at IS NULL;

COMMIT;