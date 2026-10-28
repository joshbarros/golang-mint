CREATE TABLE drops.drops (
    id                uuid PRIMARY KEY,
    code              text        NOT NULL UNIQUE CHECK (char_length(code) <= 20),
    name              text        NOT NULL,
    series            text        NOT NULL,
    artist            text        NOT NULL,
    release_date      timestamptz NOT NULL,
    price_cents       bigint      NOT NULL,
    units_total       integer     NOT NULL,
    units_left        integer     NOT NULL,
    max_per_collector integer     NOT NULL,
    created_at        timestamptz NOT NULL,
    CONSTRAINT drops_remaining CHECK (units_left >= 0 AND units_left <= units_total),
    CONSTRAINT drops_cap CHECK (max_per_collector >= 1 AND max_per_collector <= units_total)
);

-- How many units one collector holds in one drop. Written only by the reserve and release handlers.
CREATE TABLE drops.collectors (
    drop_id  uuid    NOT NULL REFERENCES drops.drops (id),
    owner_id uuid    NOT NULL,
    units    integer NOT NULL CHECK (units >= 0),
    PRIMARY KEY (drop_id, owner_id)
);

CREATE TABLE drops.unit_holds (
    order_id       uuid PRIMARY KEY,
    owner_id       uuid        NOT NULL,
    drop_id        uuid        NOT NULL REFERENCES drops.drops (id),
    units          integer     NOT NULL,
    status         text        NOT NULL,
    created_at     timestamptz NOT NULL,
    released_at    timestamptz,
    release_reason text
);

CREATE TABLE drops.outbox_messages (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    message_id  uuid        NOT NULL,
    module      text        NOT NULL,
    routing_key text        NOT NULL,
    key         text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL,
    sent_at     timestamptz
);
CREATE INDEX drops_outbox_unsent ON drops.outbox_messages (id) WHERE sent_at IS NULL;

CREATE TABLE drops.inbox_messages (
    consumer   text        NOT NULL,
    message_id uuid        NOT NULL,
    claimed_at timestamptz NOT NULL,
    PRIMARY KEY (consumer, message_id)
);
