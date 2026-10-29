CREATE TABLE crawling_data (
    id                  BIGSERIAL PRIMARY KEY,
    country_id          BIGINT NOT NULL,
    organization_id     BIGINT NOT NULL,
    site_id             BIGINT NOT NULL,
    page_id             BIGINT NOT NULL,
    category_id         BIGINT NOT NULL,
    subcategory_id      BIGINT,
    record_type         VARCHAR NOT NULL,
    title               TEXT,
    language_code       VARCHAR,
    published_at        TIMESTAMPTZ,
    content             TEXT NOT NULL,
    summary             TEXT,
    confidence          FLOAT4,
    content_hash        BYTEA NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ DEFAULT NULL,

    UNIQUE (page_id),               -- one row per page; the crawl upserts on it

    FOREIGN KEY (country_id)        REFERENCES countries(id),
    FOREIGN KEY (organization_id)   REFERENCES organizations(id),
    FOREIGN KEY (site_id)           REFERENCES sites(id),
    FOREIGN KEY (page_id)           REFERENCES pages(id),
    FOREIGN KEY (category_id)       REFERENCES categories(id),
    FOREIGN KEY (subcategory_id)    REFERENCES categories(id)
);
