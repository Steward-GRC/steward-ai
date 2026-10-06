# API

The API is `steward.ai.v1.AiService`, in [`proto/steward/ai/v1`](../proto/steward/ai/v1), with
the Go stubs committed in `gen/go`. Errors carry an `ErrorInfo` ([error codes](error-codes.md)).

## Who calls it

The gateway is the only caller. It authenticates the person, decides what they may do, and sends
the person as the [go-grpc-actor](https://github.com/Bugs5382/go-grpc-actor) actor. No request
carries a user id or a role; during act-as the actor's impersonator is the administrator, and audit
names them.

## Read scope

`ReadScope` is what the person asking may read, bound by the gateway from their access:

| Field | Meaning |
| --- | --- |
| `category_ids` | Standard documents in these categories are readable. |
| `include_sensitive` | Sensitive documents in those categories are readable too. |
| `all_categories` | Every category is readable; sensitive documents still need `include_sensitive`. |

Retrieval, citations, cached answers, top questions and related policies are filtered by the scope inside the
database query, so nothing outside it reaches the model or the answer.

## Calls

| Call | What it does |
| --- | --- |
| `SearchAndAnswer` | Answers a question from the documents the scope may read, with citations and per-segment sources. |
| `AuthoringAssist` | Suggests text for one editable region (draft, expand, rewrite, clarify, summarise). Never applied by the service. |
| `SubmitAIJob`, `GetAIJob` | Start an asynchronous job and read its state. A job reads as not found to anyone but its submitter. |
| `GetRelatedPolicies` | The policies nearest to one policy, filtered by the scope. |
| `GetTopQuestions`, `GetPolicySummary` | The questions asked most often, only those whose cited policies the scope may all read; the summary stored at publish. |
| `GetProviderStatus` | The cached provider status; never calls the provider. |
| `GetAIEnabled`, `SetAIEnabled` | The module switch. Turning it on needs the current data notice accepted. |
| `GetAIConfig` | The settings. The credential shows only as set, with its last four characters. |
| `SetProviderConfig`, `SetProviderCredential`, `TestProvider` | The provider, its settings and its credential, and a test call. |
| `AcceptDataNotice` | Records an administrator's acceptance of the data notice. |
| `SetMonthlyLimit`, `GetUsage` | The organisation's monthly request limit and the usage meter. |
| `SetOrgContext` | The organisation context put in front of every prompt; empty by default. |
| `SetAIRetrievalConfig`, `SetUserAiQueryLimit` | The retrieval candidate count; one person's daily limit. |

While the module is off, `SearchAndAnswer`, `AuthoringAssist`, `SubmitAIJob` and `TestProvider`
are refused with `AI_DISABLED`, and nothing calls a provider.

## Jobs

`SubmitAIJob` creates a `PolicyAIJob` resource (`ai.steward-grc.com/v1alpha1`, the CRD in
[`deployments/crd`](../deployments/crd)) and returns its name as the job id. The operator runs it,
stores the result and publishes a completion event. The model is fixed when the job is submitted,
so a later model change doesn't affect it.

| Operation | Submitted by | Model call |
| --- | --- | --- |
| `DRAFT`, `REVISE`, `REVIEW`, `REWRITE`, `CLARIFY`, `SUMMARIZE`, `QA` | a person | yes |
| `SUGGEST_ENRICHMENTS` | a person | only for definitions and references |
| `RELATED_REEVAL` | the service, after a publish | no |
| `RELATIONSHIP_LEARN` | the operator, nightly | no |

## Events

**In, from core.** JSON on core's `jobs` topic exchange, into two durable queues:

| Queue | Routing keys | Does |
| --- | --- | --- |
| `ai.policy.publish` | `policy.published` (publish and unpublish, told apart by `event_type`), `policy.retired` | Indexes a published version; removes an unpublished version, or every chunk of a retired policy. |
| `ai.procedure.publish` | `procedure.published`, `procedure.retired` | The same for procedures. |

Core's reindex publishes straight to these queues through the default exchange. A publish also
stores the version's summary (skipped while the module is off), recomputes the policy's centroid and
queues a related-policy re-evaluation. A message that fails twice is dropped.

**Out.** Audit events (`steward.audit.v1.AuditEvent`, protobuf binary, content type
`application/protobuf; proto=steward.audit.v1.AuditEvent`) to the `audit` exchange, for every AI
call, every settings change (never the credential) and every refused service call
(`ai.call.refused`). Job completions go to the `ai.jobs` topic exchange as `ai.job.succeeded` and
`ai.job.failed`.

## Health

`grpc.health.v1` is served without a token. The empty service name and `readiness` follow the
required dependencies; `liveness` reports the process only. Every `Check` answer carries
`steward-version`, `steward-commit`, `steward-dep-<name>` and `steward-depstate-<name>`. `/livez` and
`/readyz` on `PROBE_PORT` serve the same over HTTP, with `Steward-Version` and `Steward-Commit`. See
the [runbook](runbook.md#probes).

## Calling other services

ai calls no other service's API: the gateway resolves what a call needs from core (template
sections, attached enrichments, the person's read scope) and passes it in. It uses two other
services' contracts:

- steward-audit's `AuditEvent`, pinned by commit in [`proto-refs.env`](../proto-refs.env)
  (`STEWARD_AUDIT_REF`). `scripts/proto-generate.sh` fetches that proto into the git-ignored
  `.protos/` and generates `gen/go/thirdparty/audit/v1`; set `STEWARD_AUDIT_PROTO_DIR` to try an
  unmerged change.
- core's lifecycle events, JSON as core documents them.

`STEWARD_CORE_REF` pins steward-core's `internal/workloadauth`, which is copied here byte for byte;
`scripts/workloadauth-check.sh` fails CI when the copy differs.

## Service-to-service authentication

The gateway sends its projected service-account token (audience `steward`) on every call. ai
verifies it against the cluster's JWKS, maps the service account to a caller name, and allows only
`gateway`, on behalf of the person, on every method; any other caller is refused and audited.
`WORKLOAD_AUTH=disabled` switches this off for local runs only, and readiness then reports degraded.
