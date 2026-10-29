CREATE TABLE crawl_batches (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    status              VARCHAR NOT NULL DEFAULT 'running',
    country_ids         BIGINT[] NOT NULL DEFAULT '{}',
    countries_total     INTEGER NOT NULL DEFAULT 0,
    sites_total         INTEGER NOT NULL DEFAULT 0,
    sites_skipped       INTEGER NOT NULL DEFAULT 0,
    started_at          TIMESTAMPTZ NOT NULL,
    finished_at         TIMESTAMPTZ,
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
    batch_id            BIGINT
    
    FOREIGN KEY (batch_id) REFERENCES crawl_batches(id);
);