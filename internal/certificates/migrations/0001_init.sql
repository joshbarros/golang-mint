CREATE TABLE certificates.certificates (
    order_id   uuid PRIMARY KEY,
    owner_id   uuid        NOT NULL,
    number     text,
    status     text        NOT NULL,
    reason     text,
    created_at timestamptz NOT NULL
);

CREATE TABLE certificates.outbox_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id  uuid        NOT NULL,
    module      text        NOT NULL,
    routing_key text        NOT NULL,
    key         text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    sent_at     timestamptz
);
CREATE INDEX certificates_outbox_unsent ON certificates.outbox_messages (id) WHERE sent_at IS NULL;

CREATE TABLE certificates.inbox_messages (
    consumer   text        NOT NULL,
    message_id uuid        NOT NULL,
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, message_id)
);

