# OpenPersona Web

The web app where people build personas: bring their documents, train a knowledge graph, check what it learned, and publish it for their products. Not built yet; this is the plan.

## What a user can do

1. **Create a persona**: name, role, description, expertise, tone, language, boundaries (the library's `persona.Spec`).
2. **Add sources**: upload Markdown, text or PDF files, or paste text. Sources are versioned with the persona.
3. **Train**: start a training job and watch it (chunks extracted, units found, cost so far). Training runs in the backend with the library's `persona.Trainer`.
4. **Evaluate**: write held-out questions with the facts a good answer needs; see coverage per question, and the answers when scored end to end (`persona.Evaluate`).
5. **Explore the graph**: browse knowledge units by kind, their tags and source excerpts; search them; diff two versions.
6. **Try it**: a playground for knowledge retrieval and chat as the persona.
7. **Publish**: freeze a version (`id@version`, immutable) into the registry, set it as latest, deprecate or yank old ones.
8. **Use it**: create API keys scoped to personas and to knowledge, chat or download; see usage; copy snippets for Go, HTTP, gRPC and MCP; download the pack to run locally.

## What the backend needs for it

The serving API exists. The web app needs a "studio" API in `backend/` next to it, authenticated by user accounts rather than customer API keys:

| Area | Endpoints (sketch) |
|---|---|
| Accounts | sign-in, organizations, members |
| Personas | create, edit spec, list |
| Sources | upload (object storage), list, delete |
| Training | start a job, progress (events), cancel, result report |
| Evaluation | edit the question set, run, results |
| Units | list, search, diff versions (from the registry's `persona_units`) |
| Publishing | push a trained pack to the registry, set status |
| Keys and usage | the registry's keys and usage tables |

Training is long and costs model tokens, so it runs as an async job (the backend's job store already exists), with a budget per organization.

## Suggested stack

- Next.js and TypeScript, deployed on Vercel or Cloudflare.
- API client generated from the contract with buf and Connect for ES (see the library's [docs/GRPC.md](https://github.com/VicoAritonang/open-persona/blob/main/docs/GRPC.md)), so the web app and the backend share one schema.
- The browser never holds a customer API key: the web app talks to the studio API with the user's session.
