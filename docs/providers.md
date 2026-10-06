# Providers

Generation and embeddings go through Steward's own interface in `internal/provider`: a
`Generator` completes a request (system prompt, turns, token budget, model) and returns the text
with the tokens it used; an `Embedder` returns one 384-wide vector per text. Each provider has an
adapter in its own subpackage, and nothing outside the adapters touches a provider's SDK or wire
types. `internal/provider/build` picks the adapter: `build.New(settings)` by the settings' kind,
`build.NewEmbedder(kind, endpoint, model, credential)` for embeddings.

## Generative adapters

| Kind | Package | Model | Base URL | Region | Deployment | Credential |
| --- | --- | --- | --- | --- | --- | --- |
| `anthropic` | `anthropic` | optional, default `claude-sonnet-4-6` | optional override | - | - | required, API key |
| `openai` | `openai` | required | optional, default `https://api.openai.com/v1` | - | - | required, API key |
| `azure_openai` | `openai` | ignored, the deployment picks it | required, the resource endpoint | - | required | required, API key |
| `openai_compatible` | `openai` | required | required, the server's `/v1` root | - | - | optional |
| `gemini` | `gemini` | required | optional, default `https://generativelanguage.googleapis.com/v1beta` | - | - | required, API key |
| `bedrock` | `bedrock` | required, a Bedrock model ID | optional endpoint override | required | - | optional, `ACCESS_KEY_ID:SECRET_ACCESS_KEY` |

An empty or unknown kind, or a missing required setting, fails with `provider.ErrNotConfigured`
before any call. A refused credential (401 or 403, or the provider's equivalent) is wrapped with
`provider.ErrAuth`; `provider.Reason` turns either into the provider status reason. A request's
`Model` overrides the configured one; a request with no `MaxTokens` gets
`airules.DefaultMaxTokens`. Adapters read no environment credentials.

### Anthropic

The Messages API through `anthropic-sdk-go`, with the key sent as `x-api-key`. Calls use the
streaming endpoint and gather the events into one message: the SDK refuses a non-streaming call
whose estimated time passes ten minutes, which a drafted section at its budget does. The system
prompt carries an ephemeral cache mark, so repeated calls of one operation read it from the
cache. Input tokens include cache reads and writes.

With `EnableWebFetch` the request carries the web fetch server tool, at most five fetches:
`web_fetch_20260209` for current models, `web_fetch_20250910` for Haiku, Claude 3 and the Claude 4
models up to Sonnet 4.5 and Opus 4.5. If the API refuses the call because of the tool (a 400 or
403 naming web fetch) the call is retried once without it. The reply is the text after the last
tool block, so the model's note before a fetch isn't part of the answer.

The SDK retries 429, 5xx and connection failures on its own.

### OpenAI, Azure OpenAI and OpenAI-compatible servers

Chat Completions over `net/http`, one adapter in three modes:

- **OpenAI:** `POST {base}/chat/completions`, `Authorization: Bearer`, `max_completion_tokens`.
- **Azure OpenAI:** `POST {base}/openai/deployments/{deployment}/chat/completions?api-version=2024-10-21`
  (`openai.AzureAPIVersion`, the latest generally available dated version), the `api-key` header,
  `max_completion_tokens`, no `model` field.
- **Compatible:** `POST {base}/chat/completions`, Bearer only when a credential is set, and
  `max_tokens`, which compatible servers accept more widely than its replacement.

The system prompt goes first as a `system` message. Tokens are `prompt_tokens` and
`completion_tokens`. Web fetch is ignored. There is no retry in the adapter.

### Gemini

`POST {base}/models/{model}:generateContent` over `net/http`, with the `x-goog-api-key` header.
The system prompt is `systemInstruction`, assistant turns are sent as `model`, the budget is
`generationConfig.maxOutputTokens`. Thought parts are left out of the text; output tokens are
`candidatesTokenCount` plus `thoughtsTokenCount`, both billed as output. Gemini answers a bad key
with 400 and reason `API_KEY_INVALID`, which counts as a refused credential like 401 and 403.

### Bedrock

The Converse API through `aws-sdk-go-v2` `bedrockruntime`, signed with SigV4 in the configured
region. A credential of the form `ACCESS_KEY_ID:SECRET_ACCESS_KEY` is used as static keys;
without one the AWS default chain applies (environment, shared files, workload identity, instance
role). Bedrock reports refused keys as 403 (`UnrecognizedClientException`,
`AccessDeniedException`). Input tokens include cache reads and writes. Web fetch is ignored. The
SDK retries throttling and 5xx on its own.

## Embeddings

The schema stores 384-wide vectors; every embedder returns that width or fails.

| Backend | Package | Endpoint | Model | Credential |
| --- | --- | --- | --- | --- |
| `tei` (default) | `tei` | required, the server's full `/embed` URL | set on the server | optional, sent as Bearer |
| `openai` | `openai` | optional base URL, default OpenAI | required | optional, sent as Bearer |
| `stub` | `stub` | - | - | - |

- **tei:** a Text Embeddings Inference server running a 384-wide model such as
  `BAAI/bge-small-en-v1.5`. The batch goes in one call with `truncate: true`, so a chunk over the
  model's token limit is cut rather than failing its section. The HTTP timeout is
  `AI_EMBED_TIMEOUT_SECONDS`, default 120.
- **openai:** `POST {base}/embeddings` with `dimensions: 384`. Works with OpenAI's
  `text-embedding-3` models and with compatible servers that honour `dimensions` or serve a
  384-wide model. Vectors are put back in input order by `index`.
- **stub:** see below.

## The local stand-ins

`stub.Generator` and `stub.Embedder` are for running without any provider. They never touch the
network and are deterministic. The generator's text starts with
`[STUB AI RESPONSE: no AI provider is configured ...]`; a draft call gets plain section text and a
review-format call one `<finding>` block, so those flows show a result. The embedder hashes the
text into a normalised vector: identical text embeds identically, but the vectors carry no
meaning. `build.New` never picks the stub generator; wiring it in is an explicit local choice.

## Recorded contracts

Every adapter is tested against recorded contracts of its provider's API, in
`internal/provider/<adapter>/testdata/contracts/<provider>/`. Each JSON file holds:

- `call`: the settings (placeholder keys only) and the request handed to the adapter;
- `exchanges`: per HTTP request, what the adapter must send (method, path, query, the auth header
  and its value or prefix, other headers, the body fields that matter, fields and headers that
  must be absent) and the provider's answer (status, headers, a JSON or text body, or server-sent
  events);
- `expect`: the text and tokens, the vector count and width, or the error class (`auth`,
  `provider`).

`internal/provider/internal/contract` replays them with an `httptest` server and fails the test on
any mismatch, an extra request or an unserved exchange. Each adapter covers success with token
accounting, a refused credential, and a 5xx.

The contracts are written from each provider's published API reference, linked in each file's
`reference` field, not captured from live traffic. When a provider changes its API, update the
contract from the reference first, watch the test fail, then change the adapter. Tests never call
a live provider. The one live test, `internal/provider/anthropic/live_test.go`, builds only with
`-tags live` and runs only when `STEWARD_AI_LIVE_ANTHROPIC_KEY` is set; CI does neither.
