-- Copyright 2026 The Steward Authors
-- SPDX-License-Identifier: Apache-2.0

-- The ai service's schema. Embeddings are 384-wide (the built-in TEI model,
-- BAAI/bge-small-en-v1.5); an embeddings adapter must produce that width.

CREATE EXTENSION IF NOT EXISTS vector;

-- One row per chunk of a published document section. category_id,
-- sensitivity and policy_title are copied from core's publish event so the
-- read scope applies without a join.
CREATE TABLE ai_chunks (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id     text        NOT NULL,
    version_id    text        NOT NULL,
    version_no    integer     NOT NULL,
    section_key   text        NOT NULL,
    chunk_index   integer     NOT NULL,
    content_text  text        NOT NULL,
    embedding     vector(384) NOT NULL,
    category_id   text        NOT NULL,
    sensitivity   text        NOT NULL DEFAULT 'standard',
    policy_title  text        NOT NULL,
    indexed_at    timestamptz NOT NULL DEFAULT now(),
    document_type text        NOT NULL DEFAULT 'POLICY',
    UNIQUE (version_id, section_key, chunk_index)
);

CREATE INDEX ai_chunks_embedding_hnsw_idx
    ON ai_chunks
    USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE INDEX ai_chunks_policy_id_idx ON ai_chunks (policy_id);
CREATE INDEX ai_chunks_version_id_idx ON ai_chunks (version_id);

-- The durable answer cache, keyed by the question, the scope's hash and the
-- corpus version. ask_count backs the top questions.
CREATE TABLE ai_qa_cache (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    question_text        text        NOT NULL,
    question_embedding   vector(384) NOT NULL,
    scope_hash           text        NOT NULL,
    include_sensitive    boolean     NOT NULL DEFAULT false,
    corpus_version       bigint      NOT NULL DEFAULT 0,
    answer_text          text        NOT NULL,
    citations_json       jsonb       NOT NULL DEFAULT '[]',
    no_authorized_source boolean     NOT NULL DEFAULT false,
    has_sensitive_source boolean     NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    ask_count            integer     NOT NULL DEFAULT 1
);

CREATE INDEX ai_qa_cache_embedding_hnsw_idx
    ON ai_qa_cache
    USING hnsw (question_embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE INDEX ai_qa_cache_scope_idx
    ON ai_qa_cache (scope_hash, include_sensitive, corpus_version);

-- The durable copy of each finished job's result.
CREATE TABLE ai_job_results (
    job_id        text        PRIMARY KEY,
    operation     text        NOT NULL,
    policy_id     text        NOT NULL DEFAULT '',
    version_id    text        NOT NULL DEFAULT '',
    actor_user_id text        NOT NULL DEFAULT '',
    result_json   jsonb       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ai_job_results_actor_idx ON ai_job_results (actor_user_id, created_at DESC);

CREATE INDEX ai_job_results_policy_version_idx ON ai_job_results (policy_id, version_id) WHERE policy_id <> '';

-- The module's settings, one row. The module ships off. The credential is
-- stored sealed (AES-256-GCM under the settings key); only its last four
-- characters are kept in the clear, for display.
CREATE TABLE ai_config (
    id                   boolean     PRIMARY KEY DEFAULT true CHECK (id),
    enabled              boolean     NOT NULL DEFAULT false,
    model                text        NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT now(),
    top_k                integer     NOT NULL DEFAULT 50,
    blend_centroid       real        NOT NULL DEFAULT 0.5,
    blend_usage          real        NOT NULL DEFAULT 0.3,
    blend_crossref       real        NOT NULL DEFAULT 0.15,
    blend_entity         real        NOT NULL DEFAULT 0.05,
    provider             text        NOT NULL DEFAULT '',
    base_url             text        NOT NULL DEFAULT '',
    region               text        NOT NULL DEFAULT '',
    deployment           text        NOT NULL DEFAULT '',
    credential_sealed    bytea       NOT NULL DEFAULT '\x',
    credential_last4     text        NOT NULL DEFAULT '',
    notice_version       text        NOT NULL DEFAULT '',
    notice_accepted_by   text        NOT NULL DEFAULT '',
    notice_accepted_at   timestamptz,
    monthly_limit        bigint      NOT NULL DEFAULT 0 CHECK (monthly_limit >= 0),
    org_context          text        NOT NULL DEFAULT ''
);

INSERT INTO ai_config (id) VALUES (true);

-- Summaries generated when a version is published.
CREATE TABLE ai_summaries (
    version_id    text        PRIMARY KEY,
    policy_id     text        NOT NULL,
    policy_title  text,
    summary_text  text        NOT NULL,
    generated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ai_summaries_policy_id_idx ON ai_summaries (policy_id);

-- Per-person daily query limit overrides; -1 is unlimited.
CREATE TABLE ai_user_query_limit (
    user_id     text PRIMARY KEY,
    daily_limit integer NOT NULL CHECK (daily_limit = -1 OR daily_limit >= 1),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One centroid per policy: the mean of its published version's chunk
-- embeddings.
CREATE TABLE ai_policy_centroids (
    policy_id      text        PRIMARY KEY,
    version_id     text        NOT NULL,
    version_no     integer     NOT NULL,
    centroid       vector(384) NOT NULL,
    chunk_count    integer     NOT NULL,
    category_id    text        NOT NULL,
    sensitivity    text        NOT NULL DEFAULT 'standard',
    policy_title   text        NOT NULL,
    corpus_version bigint      NOT NULL DEFAULT 0,
    computed_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ai_policy_centroids_hnsw_idx
    ON ai_policy_centroids
    USING hnsw (centroid vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE INDEX ai_policy_centroids_category_idx ON ai_policy_centroids (category_id);

-- Suggested related policies. accepted and dismissed rows stay, so a
-- decided suggestion is never proposed again.
CREATE TABLE ai_related_policies (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_id       text        NOT NULL,
    related_id      text        NOT NULL,
    score           real        NOT NULL,
    centroid_dist   real        NOT NULL,
    source          text        NOT NULL,
    status          text        NOT NULL DEFAULT 'suggested',
    corpus_version  bigint      NOT NULL,
    suggested_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (policy_id, related_id)
);

CREATE INDEX ai_related_policies_policy_idx ON ai_related_policies (policy_id, status);

-- The weighted relationship between two policies (policy_a < policy_b).
CREATE TABLE ai_policy_relationship (
    policy_a        text        NOT NULL,
    policy_b        text        NOT NULL,
    centroid_sim    real        NOT NULL DEFAULT 0,
    usage_weight    real        NOT NULL DEFAULT 0,
    crossref_weight real        NOT NULL DEFAULT 0,
    entity_weight   real        NOT NULL DEFAULT 0,
    blended_score   real        NOT NULL DEFAULT 0,
    co_retrieval_n  integer     NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (policy_a, policy_b)
);
CREATE INDEX ai_policy_relationship_a_idx ON ai_policy_relationship (policy_a, blended_score DESC);
CREATE INDEX ai_policy_relationship_b_idx ON ai_policy_relationship (policy_b, blended_score DESC);

-- The usage meter: provider requests and tokens per calendar month (UTC).
CREATE TABLE ai_usage_monthly (
    month         date        PRIMARY KEY,
    requests      bigint      NOT NULL DEFAULT 0,
    input_tokens  bigint      NOT NULL DEFAULT 0,
    output_tokens bigint      NOT NULL DEFAULT 0,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
