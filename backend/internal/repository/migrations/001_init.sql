CREATE TABLE IF NOT EXISTS chats (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id      TEXT        NOT NULL,
    title         TEXT        NOT NULL DEFAULT 'Новый чат',
    message_count INTEGER     NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chats_owner ON chats (owner_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_chats_updated ON chats (updated_at);

CREATE TABLE IF NOT EXISTS messages (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id    UUID        NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    seq        INTEGER     NOT NULL,
    role       TEXT        NOT NULL CHECK (role IN ('user', 'assistant')),
    kind       TEXT        NOT NULL DEFAULT 'text'
               CHECK (kind IN ('text', 'answer', 'clarification', 'refusal')),
    content    TEXT        NOT NULL,
    meta       JSONB       NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(meta) = 'object'),
    latency_ms INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT messages_chat_seq UNIQUE (chat_id, seq),
    CONSTRAINT messages_role_kind CHECK (
        (role = 'user' AND kind = 'text') OR (role = 'assistant' AND kind <> 'text')
    )
);
CREATE TABLE IF NOT EXISTS message_sources (
    message_id   UUID     NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    ordinal      SMALLINT NOT NULL,
    doc_id       TEXT     NOT NULL,
    doc_title    TEXT     NOT NULL,
    fragment_id  TEXT     NOT NULL,
    text         TEXT     NOT NULL,
    start_offset INTEGER,
    end_offset   INTEGER,
    PRIMARY KEY (message_id, ordinal)
);
