# Error codes

Every coded gRPC error from the ai service carries an `ErrorInfo` with the symbol as
its reason, the domain `ai` and the code in `codeNum`. Only user-safe messages reach
the caller; every other code is sent as `Code N: Internal Error`.

| Code | Symbol | Area | Cause | User-safe |
| --- | --- | --- | --- | --- |
| 3000 | `INTERNAL` | ai | an uncoded failure inside the ai service | no |
| 3001 | `AI_QUOTA_EXCEEDED` | quota | the person reached their daily query limit (limit, used and reset_at in the metadata) | yes |
| 3002 | `AI_CATEGORY_FORBIDDEN` | scope | the request narrowed the search to a category its read scope doesn't hold (category_id in the metadata) | yes |
| 3003 | `AI_RETRIEVAL_UNAVAILABLE` | retrieval | embedding the question or the vector search failed (op in the metadata); the cause is in the debug log | no |
| 3004 | `AI_GENERATION_FAILED` | generation | the provider call failed; the cause is in the debug log | no |
| 3005 | `AI_DISABLED` | module | the AI module is off, so no AI call is accepted | yes |
| 3006 | `AI_DATA_NOTICE_REQUIRED` | module | the module can't be turned on before an administrator accepts the current data notice | yes |
| 3007 | `AI_MONTHLY_LIMIT_REACHED` | usage | the organisation's monthly request limit is reached (limit and reset_at in the metadata) | yes |
| 3008 | `AI_ACTOR_REQUIRED` | request | the call carries no go-grpc-actor actor, so there is nobody to answer or attribute it to | no |
| 3009 | `AI_REQUEST_INVALID` | request | a required field is missing or a value is out of range (field and reason in the metadata); the gateway validates first, so this is a client bug | no |
| 3010 | `AI_JOB_NOT_FOUND` | job | no job has this id, or it was submitted by someone else | yes |
| 3011 | `AI_PROVIDER_NOT_CONFIGURED` | provider | no provider is chosen, or the chosen one needs a credential, endpoint or region that isn't set | yes |
| 3012 | `AI_SETTINGS_UNAVAILABLE` | settings | the module's settings couldn't be read, so the call is refused rather than run with the module possibly off | yes |
| 3013 | `AI_JOBS_UNAVAILABLE` | job | the service has no Kubernetes API to create or read PolicyAIJob resources; the reason is in the start-up log | yes |
