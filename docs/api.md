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
| `all_categories` | Every document is readable, sensitive ones included. |

Retrieval, citations, cached answers and related policies are filtered by the scope inside the
database query, so nothing outside it reaches the model or the answer.

## Calls

| Call | What it does |
| --- | --- |
| `SearchAndAnswer` | Answers a question from the documents the scope may read, with citations and per-segment sources. |
| `AuthoringAssist` | Suggests text for one editable region (draft, expand, rewrite, clarify, summarise). Never applied by the service. |
| `SubmitAIJob`, `GetAIJob` | Start an asynchronous job and read its state. A job reads as not found to anyone but its submitter. |
| `GetRelatedPolicies` | The policies nearest to one policy, filtered by the scope. |
| `GetTopQuestions`, `GetPolicySummary` | The questions asked most often; the summary stored at publish. |
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
