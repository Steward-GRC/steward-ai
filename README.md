# steward-ai 🐹

> 🧭 AI service for Steward: cited answers, drafting, review and summaries behind one provider interface

## 🎯 What it is

The AI module of Steward. It answers questions from the policies and procedures the person asking
may read, with citations; suggests text while an author edits; drafts, revises, reviews and
summarises documents as jobs run by its operator; and suggests related policies. It indexes what
core publishes, and the gateway is its only caller.

- 🔒 **AI only sees what the asker can read:** every read is filtered by the asker's category scope
  inside the database query.
- 🔌 **Any provider:** Anthropic (the default), OpenAI, Azure OpenAI, Google Gemini, AWS Bedrock or
  any OpenAI-compatible server, behind one interface; embeddings from the built-in TEI server.
- 🛑 **Off until it's turned on:** a fresh install ships with AI off; turning it on needs the data
  notice accepted. A usage meter and a monthly limit come with it.

## 🚀 Run

```bash
cp .env.example .env   # Postgres (pgvector), RabbitMQ, Valkey and a settings key
task run               # the gRPC service; task run-operator for the job operator
```

The images: `docker build --build-arg VERSION=<tag> --build-arg COMMIT=<sha> .`, and the same with
`-f Dockerfile.operator`; `Dockerfile.embeddings` builds the embeddings server.

## 📚 More

- [API, jobs and health](docs/api.md)
- [Providers](docs/providers.md)
- [Configuration](docs/configuration.md)
- [Data and migrations](docs/data.md)
- [Error codes](docs/error-codes.md)
- [Runbook](docs/runbook.md)
- [Contributing](https://github.com/Steward-GRC/.github/blob/main/.github/CONTRIBUTING.md) and
  [security](https://github.com/Steward-GRC/.github/blob/main/.github/SECURITY.md)

## 🛠 Develop

```bash
task build     # go build ./...
task test      # go test ./... (Postgres, RabbitMQ and Valkey tests need Docker)
task lint      # gofmt check + golangci-lint + yamllint
task proto     # fetch the pinned callee protos and regenerate gen/
task generate  # regenerate the CRD and deepcopy code
task license   # check Apache-2.0 headers (golic)
```

## 🙏 Acknowledgements

Steward was originally written by [@Bugs5382](https://github.com/Bugs5382).

## ⚖️ License

Apache-2.0 (c) 2026 The Steward Authors
