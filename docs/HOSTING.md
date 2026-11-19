# Hosting OpenPersona

The hosted platform serves personas to customers who hold an API key: knowledge, answers, async jobs, pack downloads, MCP and gRPC. Customers use it from Go with the library's client (`openpersona.NewClient`, see the library's [docs/CLIENT.md](../../docs/CLIENT.md)), or from any language over HTTP or gRPC (the contract is the library's [`api/`](../../api)).

## The hosted service

```
            customers' backends (Go library, HTTP, gRPC, MCP)  frontend (Vercel / Cloudflare)
                         │                     │                       │
       api.example.com ──┤                     └── stream.example.com  │
                         ▼                                 ▼           │
           ┌───────────────────────────┐        ┌───────────────────────┐
           │ AWS Lambda (Function URL) │        │ EC2 t3.small + Caddy  │
           │  knowledge, chat, jobs,   │        │  openpersona-cloud    │
           │  pack download, MCP,      │        │  serve: streaming,    │
           │  Connect RPC (unary)      │        │  gRPC, MCP, jobs      │
           │  ── async invoke ──┐      │        │                       │
           │  worker: RunJob ◄──┘      │        └───────────┬───────────┘
           └─────────────┬─────────────┘                    │
                         └──────────────┬───────────────────┘
                                        ▼
                    Neon Postgres (schema openpersona): packs, versions,
                    units, api_keys, jobs, usage_events
```

| Piece | Role | Why there |
|---|---|---|
| Lambda (`cmd/openpersona-lambda`) | All non-streaming traffic, and every async job | Go starts in tens of milliseconds and the function scales to zero. One function: an HTTP request that creates a job stores it in Neon and invokes the same function with `InvocationType=Event`; that invocation claims the job, runs it, writes the result. |
| EC2 t3.small (`openpersona-cloud serve`) | Streaming (`"stream": true`, `ChatStream`), native gRPC, and a second home for the whole API | A buffered Function URL cannot stream, SSE needs a long-lived connection, and native gRPC needs HTTP/2. 2 GB is plenty: the Go server takes tens of MB, packs are memory-mapped. |
| Neon | Packs and every piece of shared state | Both compute tiers are stateless: keys, jobs and usage are rows, and packs are pulled into local disk (`/tmp` on Lambda, `/var/cache/openpersona` on EC2) on first use, then cached by SHA-256. |

Both tiers run the same handler (`server.Server`), so behaviour cannot drift; they differ only in wiring (`Dispatch` sends jobs to Lambda; on EC2 they run in a goroutine).

### Cold start

A cold Lambda opens its Neon pool in the init phase, then pulls a pack the first time a persona is used: for Kora (14 MB) the first request took about 14 s in a local test against Neon, later ones 0.6 s. To keep that off customers' paths:

- Ping each persona's `GET /v1/personas/{id}` after every deploy and every few minutes (an EventBridge schedule), or set provisioned concurrency to 1.
- Or bundle hot packs into the deployment package and point `Catalog.Dir` at them (Lambda zips may be 250 MB unzipped).
- Larger packs belong in S3/R2 instead of `bytea` (see REGISTRY.md).

### Deploying

1. Database: `openpersona-cloud registry migrate` (adds `api_keys`, `jobs`, `usage_events`; a schema created by running `001_init.sql` by hand is recognised and recorded).
2. Lambda (needs the AWS SAM CLI):
   ```sh
   cd deploy/aws/lambda && ./build.sh && sam deploy --guided
   ```
   The template sets arm64 / `provided.al2023`, 1024 MB, a 600 s timeout (jobs get 9 min), 2 GB of `/tmp`, a Function URL with application-level auth, one retry for async events, and permission for the function to invoke itself. Parameters carry the Neon URL and model keys (NoEcho); moving them to Secrets Manager is the next hardening step.
3. EC2 (t3.small, Amazon Linux 2023 or Ubuntu):
   ```sh
   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o deploy/aws/ec2/openpersona-cloud ./cmd/openpersona-cloud
   scp -r deploy/aws/ec2 ec2-user@<host>:~/openpersona && ssh ec2-user@<host> 'sudo ~/openpersona/install.sh'
   ```
   Fill `/etc/openpersona/openpersona.env`, install Caddy with `deploy/aws/ec2/Caddyfile` (TLS; HTTP/2 to the server so gRPC works; `flush_interval -1` so events are not buffered), and open ports 80/443 only.
4. DNS: `api.example.com` → the Function URL (through CloudFront to use your own domain), `stream.example.com` → the EC2 Elastic IP. Customers set `BaseURL` and `StreamURL`.
5. Keys, per customer:
   ```sh
   openpersona-cloud keys create -owner "Acme Travel" -name production -personas kora -scopes knowledge,chat
   openpersona-cloud keys create -owner "Acme Travel" -name self-hosted -personas kora -scopes knowledge,download
   openpersona-cloud keys list | revoke -id key_... | usage -since 720h
   ```
   The key is shown once; only its SHA-256 is stored.

### Frontend

A frontend on Vercel or Cloudflare Pages calls the same API. Set `OPENPERSONA_CORS_ORIGINS` (both tiers) to its origin. A browser must never hold a customer's production key: give the frontend its own key with the narrowest scopes, or route through the frontend's server side (Vercel functions, Cloudflare Workers) which holds the key.

## Limits and next steps

- **Ranking.** In a live probe, two facts Kora has ("Nagoya to Kariya by train", "a Swedish passport needs no visa for Japan") were in the top 20 but not the top 6. The tool therefore uses the pack's own budget (20 for Kora) unless told otherwise. About 8% of Kora's units were written in English by the extractor although the sources are Indonesian, which hurts lexical matching; the fix is a retrain (1.0.1) that pins the unit language to the spec's.
- **Rate limits and quotas per key** are not enforced yet; usage is recorded per key, persona and operation (`keys usage`), which is what billing and quotas will read.
- **Webhooks** for finished jobs (instead of polling) and **signed packs** (so a local customer can verify the publisher) are not built yet.
- Memory (`memory.Engine`) is not part of the hosted service: Lambda's disk is ephemeral. `openpersona serve` (the library's self-hosted server) keeps it.
