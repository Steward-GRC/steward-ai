# Runbook

## What runs

| Binary | Image | Replicas | Does |
| --- | --- | --- | --- |
| `cmd/server` | `Dockerfile` | one or more | The gRPC API, the indexing consumers, migrations at start-up. |
| `cmd/operator` | `Dockerfile.operator` | one or more, leader-elected | Runs `PolicyAIJob` resources: generation, results, completion events, the nightly relationship job. |
| embeddings | `Dockerfile.embeddings` | one or more | TEI with the model baked in; no network needed. |

The server creates and reads `PolicyAIJob` resources in `AI_JOB_NAMESPACE`; the operator watches,
updates and deletes them there, and holds a Lease for leader election. Both need a Role for
`policyaijobs.ai.steward-grc.com` (and the status subresource) in that namespace; the release
charts carry it.

## Probes

`/livez` reports the process only. `/readyz`, and the gRPC `grpc.health.v1` check (the empty name
and `readiness`), fail while a required dependency is down:

| Dependency | Required | Server | Operator |
| --- | --- | --- | --- |
| `postgres` | yes | yes | yes |
| `rabbitmq` | yes | yes | yes |
| `valkey` | yes | yes | yes |
| `jwks` (while `WORKLOAD_AUTH` is on) | yes | yes | no |
| `kubernetes` (the API server) | yes | no | yes |
| `embeddings` | no, degraded | yes | yes |
| `provider` (last known status, never a probe call) | no, degraded | yes | yes |
| `workloadauth` (only while `WORKLOAD_AUTH=disabled`) | no, degraded | yes | no |

Pings are short and cached for a few seconds, so readiness recovers on its own when the dependency
returns. The `liveness` gRPC service name reports the process only.

## Build information

Every `Health/Check` answer carries `steward-version`, `steward-commit`, and
`steward-dep-<name>` with each dependency's version where it reports one (`postgres` from
`SHOW server_version`); `/livez` and `/readyz` carry `Steward-Version` and `Steward-Commit`. The
images take `VERSION` (the tag) and `COMMIT` (the full SHA) build arguments; an unstamped build
reports `dev`. To read them:

```bash
grpcurl -plaintext -v <host>:9090 grpc.health.v1.Health/Check | grep -i steward-
curl -si http://<host>:8080/livez | grep -i steward-
```

## The module switch

A fresh install is off. To turn it on, through the gateway's admin settings or directly:

1. `SetProviderConfig` and `SetProviderCredential`;
2. `AcceptDataNotice` with the current version (`GetAIConfig` shows it);
3. `SetAIEnabled(true)`, then `TestProvider`.

While off, no AI call is accepted, no provider is called, the publish consumer still indexes with
the built-in embeddings but skips summaries, and the operator fails jobs instead of starting them.
After turning AI on again, ask core to reindex (`ReindexAllPublished`) if an external embeddings
provider was in use, since nothing was indexed while it was off.

## Limits and usage

`GetUsage` shows this month's provider requests and tokens. When the monthly limit is reached,
intake is refused with `AI_MONTHLY_LIMIT_REACHED` until the first day of next month (UTC), or until
the limit is raised. Each person's daily limit (`AI_QUERY_QUOTA_DEFAULT`, overridden per person) is
counted in Valkey and resets at 00:00 UTC; a Valkey error lets the call through.

## Rotating the credential or the settings key

- **Provider credential:** `SetProviderCredential` with the new one, then `TestProvider`. The old
  one is overwritten; every change is audited.
- **Settings key** (`AI_SETTINGS_KEY`): there is no re-sealing. Change the key, then set the
  credential again; until then `GetAIConfig` and every provider call fail with
  `AI_SETTINGS_UNAVAILABLE`.

## One-off: centroid backfill

Run the server image once with `AI_BACKFILL_CENTROIDS=true` (as a Job): it computes a centroid for
every indexed policy and exits. Safe to run again.
