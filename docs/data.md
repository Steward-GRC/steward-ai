# Data

The ai service keeps its own Postgres database, with the `vector` extension (pgvector). The schema
is one migration, [`migrations/0001_baseline.up.sql`](../migrations/0001_baseline.up.sql), applied
at start-up with go-postgres. Until the first release it stays the only migration; installs of the
original service come over through `steward-migrate`.

| Table | Holds |
| --- | --- |
| `ai_chunks` | One row per chunk of a published section: text, 384-wide embedding, and the category, sensitivity and title copied from core's publish event. |
| `ai_policy_centroids` | One centroid per policy: the mean of its published version's chunk embeddings. |
| `ai_related_policies` | Suggested related policies. Accepted and dismissed rows stay, so a decided suggestion isn't proposed again. |
| `ai_policy_relationship` | The weighted relationship between two policies, rebuilt by `RELATIONSHIP_LEARN`. |
| `ai_qa_cache` | The durable answer cache, keyed by question, scope hash and corpus version; `ask_count` backs the top questions. |
| `ai_summaries` | The summary generated when a version is published. |
| `ai_job_results` | The durable copy of each finished job's result. |
| `ai_user_query_limit` | Per-person daily limit overrides; -1 is unlimited. |
| `ai_usage_monthly` | The usage meter: provider requests and tokens per calendar month (UTC). |
| `ai_config` | The module's settings, one row. |

## Read scope

Every query that returns document content takes the caller's read scope and applies it in SQL: a
row is readable when the scope reads every category, or the row's category is in the scope and it
is standard or the scope includes sensitive documents. `airules.ChunkReadable` states the same rule
in Go. Top questions are suggested only when every policy the answer cited is readable.

## Settings and the credential

`ai_config` ships with the module off. The provider credential is sealed with AES-256-GCM under
`AI_SETTINGS_KEY` (32 bytes, base64), with the column name as additional data; only its last four
characters are stored in the clear, for display. A credential sealed under another key reads as an
error, never as an empty credential.

## Differences from the original schema

Proved by applying the original ten migrations and the baseline to two empty databases and diffing
`pg_dump --schema-only`. The only differences:

- `group_id` is `category_id` on `ai_chunks` and `ai_policy_centroids` (and its index), and
  `ai_qa_cache.groups_hash` is `scope_hash`: core's events carry the category;
- `ai_audit_log` is gone: audit events go to steward-audit;
- `ai_config.token` is replaced by the sealed credential and its last four, plus the provider,
  base URL, region, deployment, data notice, monthly limit and organisation context columns, and
  `enabled` defaults to false;
- `ai_usage_monthly` is new.
