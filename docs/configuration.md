# Configuration

Both binaries read their settings from the environment through `internal/config`, and a bad value
fails the boot. [`.env.example`](../.env.example) has a local set.

## Shared by the server and the operator

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_DSN` | required | Postgres with pgvector; start-up stops before migrating when the server has no `vector` extension (see [data](data.md)). |
| `RABBITMQ_URL` | required | RabbitMQ: core's publish events in, audit and job completion events out. |
| `REDIS_ADDR` | required | Valkey: the answer cache, the daily counters, the re-evaluation queue and job results. |
| `REDIS_PASSWORD` | empty | Valkey password. |
| `AI_SETTINGS_KEY` | required | 32 bytes, base64. Seals the provider credential in `ai_config`. Losing it means setting the credential again. |
| `PROBE_PORT` | `8080` | `/livez` and `/readyz`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | OpenTelemetry collector (traces and metrics). |
| `AI_EMBED_PROVIDER` | `tei` | `tei` (the built-in server), `openai` (any OpenAI-compatible embeddings endpoint) or `stub` (local runs only). |
| `EMBED_ENDPOINT` | `http://steward-ai-embeddings/embed` | The embeddings endpoint. |
| `AI_EMBED_MODEL` | empty | The embeddings model; required for `openai`. It must return 384-wide vectors. |
| `AI_EMBED_API_KEY` | empty | The embeddings endpoint's key, for `openai`. |
| `AI_EMBED_TIMEOUT_SECONDS` | `120` | The embeddings request timeout. |
| `AI_GENERATION_STUB` | `false` | Answer every call with a labelled stand-in instead of a provider. Local runs only. |
| `AI_JOB_NAMESPACE` | `POD_NAMESPACE`, else `steward` | Where `PolicyAIJob` resources live. |
| `LOG_LEVEL`, `LOG_FORMAT` | `error`, `json` | go-log. Local runs use `trace` and `console`. |

## Server

| Variable | Default | Meaning |
| --- | --- | --- |
| `GRPC_PORT` | `9090` | The gRPC API. |
| `MIGRATE_DSN` | `DATABASE_DSN` | A direct connection for migrations. |
| `MIGRATIONS_DIR` | `migrations` | The migrations directory (`/migrations` in the image). |
| `AI_CHUNK_SIZE` | `350` | Approximate words per chunk. |
| `AI_CHUNK_OVERLAP` | `64` | Approximate words of overlap between chunks. |
| `AI_RETRIEVAL_TOP_K` | `50` | Candidate chunks per question when no override is stored. |
| `AI_QUERY_QUOTA_DEFAULT` | `50` | Daily queries per person without an override; `-1` is unlimited. |
| `AI_BACKFILL_CENTROIDS` | `false` | Compute a centroid for every indexed policy, then exit. Run as a one-off Job. |
| `WORKLOAD_AUTH` | `enabled` | `disabled` turns service-to-service authentication off, for local runs only. |
| `WORKLOAD_OIDC_ISSUER`, `WORKLOAD_OIDC_JWKS_URL`, `WORKLOAD_OIDC_CA_FILE`, `WORKLOAD_OIDC_BEARER_FILE` | | The cluster issuer whose JWKS verifies callers' tokens. |
| `WORKLOAD_AUDIENCE` | `steward` | The token audience. |
| `WORKLOAD_ALLOWED_SERVICEACCOUNTS` | | The callers: `<namespace>/steward-gateway`. |

## Operator

| Variable | Default | Meaning |
| --- | --- | --- |
| `METRICS_PORT` | `9090` | controller-runtime metrics. |
| `ENABLE_LEADER_ELECTION` | `true` | One replica reconciles at a time; `false` for a local run outside a cluster. |
| `AI_JOB_MAX_ATTEMPTS` | `3` | Attempts before a job fails for good. |
| `AI_JOB_SUCCEEDED_TTL` | `15m` | How long a succeeded job's resource is kept once its result is stored; `0s` keeps it. |
| `AI_JOB_FAILED_TTL` | `720h` | How long a failed job's resource is kept; `0s` keeps it. |
| `AI_DRAFT_SECTION_MAX_TOKENS` | `32000` | Output budget per drafted section. |
| `AI_REVISE_MAX_TOKENS` | `32000` | Output budget per revision. |

## In the admin settings, not the environment

The module switch, the provider, its model, endpoint, region, deployment and credential, the data
notice, the monthly limit, the organisation context and the retrieval candidate count are set
through the API (`SetAIEnabled`, `SetProviderConfig`, `SetProviderCredential`, `AcceptDataNotice`,
`SetMonthlyLimit`, `SetOrgContext`, `SetAIRetrievalConfig`) and kept in `ai_config`. A fresh install
starts with the module off and no provider.
