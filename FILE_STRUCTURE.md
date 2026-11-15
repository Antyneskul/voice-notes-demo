# File structure and context map

Quick orientation for anyone (human or AI) opening this repo cold. For setup and usage see [README.md](README.md); for the production design see [ADR_001_Architecture.md](ADR_001_Architecture.md); for the roadmap see [NEXT_STEPS.md](NEXT_STEPS.md). This file says **what each file does and where to look to change something**.

## What this is

A demo: a sales rep records a voice note on an iPhone, a local Go/Gin service sends the audio to OpenAI (`gpt-audio-1.5`), validates the extracted **summary / action items / tags**, and creates a child page in Notion.

```
Expo app (iOS)  ->  POST /notes (Go/Gin)  ->  OpenAI chat completions (audio in, JSON out)  ->  Notion page  ->  201 + JSON
```

It implements the **Demo** section of the ADR. It is deliberately synchronous, unauthenticated and single-service. The ADR describes the production design (S3, PostgreSQL job queue, worker, speech-to-text then LLM, rep review, CRM adapters).

## Top level

| Path | Purpose |
| --- | --- |
| `README.md` | Setup, curl example, error table, known limits of the demo |
| `ADR_001_Architecture.md` | Architecture decision record: production design, demo-vs-production table, proposed API |
| `NEXT_STEPS.md` | Roadmap in priority order: evals, content validation, hardening, security, deployment, observability, cost routing |
| `FILE_STRUCTURE.md` | This file |
| `backend/` | Go service (run this first) |
| `mobile/` | Expo / React Native demo screen (optional, iOS only) |
| `samples/` | `sample-note.wav` (18 s, synthetic macOS `say` voice) and `sample-note.txt` (its script) for the curl example |
| `docs/` | `architecture-production.svg` / `.png` (ADR diagram), `notion-export-example.png` (README screenshot) |
| `.gitignore` | Ignores `.env`, `node_modules`, `.expo`, Go build output, `.DS_Store` |

## Backend (`backend/`, Go 1.25+, package `main`)

Every source file except `main.go` has a `_test.go` next to it. Tests run the real OpenAI and Notion clients against local `httptest` servers, so they check the exact requests sent and need no credentials.

| File | What it does |
| --- | --- |
| `main.go` | Entry point. Loads `.env` (godotenv), builds config, wires the real clients in `buildServer`, starts the HTTP server with timeouts, handles graceful shutdown (15 s). Holds the timeout constants: OpenAI 60 s, Notion 15 s, read 60 s. |
| `config.go` | `loadConfig(getenv)`: reads env vars, reports every missing required one by name (never the value), checks `NOTION_PARENT_PAGE_ID` is a 32-char hex ID. |
| `server.go` | `Server`, the `Extractor` and `Exporter` interfaces, `Router()` (routes `GET /healthz`, `POST /notes`), and `createNote`, the whole request path. Also `upstreamFailed` (maps errors to status and code), `accessLog`, `recoverPanic`. |
| `audio.go` | `detectAudioFormat`: decides WAV or MP3 **from the bytes**, not the filename or Content-Type. Walks WAV chunks and checks MP3 Layer III frames, so truncated or empty files fail fast with 415. A sanity check, not a decoder. |
| `openai.go` | **The LLM step.** `extractionPrompt` and `OpenAIExtractor.Extract`: one Chat Completions call with the audio base64-encoded as `input_audio`, `modalities: ["text"]`, `store: false`. Rejects a refusal, a `length` finish reason or an empty `choices`, then hands the text to `ParseNote`. |
| `note.go` | `Note` type, size limits, and **`ParseNote`**: strict validation of the model's JSON (exact keys, no duplicates, no trailing text, length limits in UTF-16 units to match Notion, tags lowercased and de-duplicated and capped at 5). Lenient only about a markdown code fence. Returns `ErrInvalidNote`. |
| `notion.go` | `NotionExporter.CreatePage`: creates a child page titled `Voice note <UTC time>` with Summary, Action items (unchecked to-dos) and Tags. `noteBlocks` builds the layout. Pins `Notion-Version`. |
| `upstream.go` | `postJSON` (shared HTTP helper for both providers), `UpstreamError` (keeps only service, status and a short code, never the body), and the `knownCodes` allow-list that controls what may be logged. |

### Request path (`createNote` in `server.go`)

1. Reject over-size requests early (limit 10 MiB audio, 11 MiB request) -> 413.
2. Parse multipart; require exactly one file in the `audio` field -> 400.
3. Read the bytes, run `detectAudioFormat` -> 415 if not valid WAV or MP3.
4. `Extractor.Extract` (OpenAI) -> `ParseNote`. Failure -> 502 `openai_failed` or `model_output_invalid`, 504 `openai_timeout`.
5. `Exporter.CreatePage` (Notion) -> 502 `notion_failed`, 504 `notion_timeout`. **Nothing is written to Notion unless validation passed.**
6. Return **201** with `{summary, action_items, tags, notion_page_url}` only after Notion confirms.

Errors are always `{"error": {"code": "...", "message": "..."}}`. The full code table is in the README.

### Backend tests

| File | Covers |
| --- | --- |
| `e2e_test.go` | Real router + real clients against local doubles: upload -> OpenAI request -> validation -> Notion request -> 201; nothing reaches Notion on invalid model output |
| `server_test.go` | Handler: upload validation, size limits, status mapping, timeouts, client disconnect, log hygiene (fakes via the two interfaces) |
| `openai_test.go` | Exact request shape, invalid model output, upstream errors (only status and code kept), timeout, caller cancellation, prompt still matches the tag limit |
| `notion_test.go` | Page created under the parent, layout with no action items or tags, upstream errors, reply without a URL, timeout |
| `note_test.go` | `ParseNote` accept and reject cases, limits, Unicode lengths |
| `audio_test.go` | WAV and MP3 detection, truncated and placeholder-size files |
| `config_test.go` | Env parsing and the page ID check |
| `upstream_test.go` | `postJSON`, error code allow-list |

### Configuration (`backend/.env.example`)

Required: `OPENAI_API_KEY`, `NOTION_API_KEY`, `NOTION_PARENT_PAGE_ID`. Optional: `PORT` (8080), `OPENAI_MODEL` (`gpt-audio-1.5`), `OPENAI_BASE_URL`, `NOTION_BASE_URL` (no trailing `/v1`). The base URL overrides are how tests and proxies point the clients elsewhere.

## Mobile (`mobile/`, Expo SDK 58, TypeScript, iOS only)

| File | What it does |
| --- | --- |
| `index.ts` | Registers `App` as the root component |
| `App.tsx` | The single screen: Record / Stop / Upload, timer, backend URL field, result card with an **Open in Notion** button. Records 16 kHz mono 16-bit WAV on iOS, auto-stops at 4 minutes, inspects the file header after Stop to show what was really recorded and disables Upload if it is not WAV or MP3. |
| `uploadNote.ts` | `uploadNote`: POSTs the multipart form to `<baseUrl>/notes` with a timeout (90 s from `App.tsx`), turns every failure into a user-readable message, validates the response. `fetch` is injected so failure paths can be tested. |
| `noteResult.ts` | `asNoteResult`: narrows the untrusted JSON response to the `NoteResult` type (and requires an http(s) Notion URL) so a wrong backend cannot crash the result screen |
| `apiUrl.ts` | `resolveApiUrl`: picks the backend URL. Order: `EXPO_PUBLIC_API_URL`, then the host Expo loaded from + port 8080, then `http://localhost:8080` |
| `*.test.ts` | `apiUrl`, `noteResult`, `uploadNote` tests; run with Node's built-in runner (`npm test`, needs Node 22.18+) |
| `app.json`, `tsconfig.json`, `package.json` | Expo and TypeScript config, dependencies, scripts (`start`, `ios`, `test`) |
| `.env.example` | Optional `EXPO_PUBLIC_API_URL` override (never put secrets here: Expo inlines it into the app) |

Android is not supported by the demo: `expo-audio` cannot record WAV or MP3 there (it produces `.m4a`).

## Where to change what

| I want to change... | Look at |
| --- | --- |
| The LLM prompt or the model call | `backend/openai.go` (`extractionPrompt`, `Extract`); model name via `OPENAI_MODEL` |
| What counts as a valid model answer | `backend/note.go` (`ParseNote`, limits) |
| What gets written to Notion / page layout | `backend/notion.go` (`noteBlocks`) |
| Accepted audio formats or file checks | `backend/audio.go`, plus `inspectRecording` in `mobile/App.tsx` |
| Status codes, error messages, size limits | `backend/server.go` |
| Timeouts | `backend/main.go` (constants), `UPLOAD_TIMEOUT_MS` in `mobile/App.tsx` |
| Retry, idempotency, auth, request IDs (planned) | `backend/server.go` and `upstream.go`; see "Demo hardening" in `NEXT_STEPS.md` |
| Swap the AI provider or the export target | Implement `Extractor` or `Exporter` (interfaces in `backend/server.go`) and wire it in `buildServer` in `main.go` |
| How the app finds the backend | `mobile/apiUrl.ts` |
| Recording settings | `RECORDING_OPTIONS` in `mobile/App.tsx` |

## Design rules to keep when editing

- **No content in logs.** A request logs only method, path, status and duration. Upstream failures log only an HTTP status and a code from the `knownCodes` allow-list; anything else is logged as `unrecognized`. Model output, audio and note text must never reach logs or error messages (`ErrInvalidNote` messages deliberately exclude model output).
- **Validate before writing.** The Notion write happens only after `ParseNote` succeeds; the 201 is returned only after Notion confirms.
- **Audio stays in memory** (`MaxMultipartMemory` is raised so Go does not spill uploads to temp files) and is never written to disk. `store: false` is set on the OpenAI call.
- **Format is decided from bytes**, never from the filename or Content-Type.
- **Interfaces for seams.** `Extractor` and `Exporter` let tests use fakes without a mocking library.
- The prompt treats the recording as data, not instructions, and asks for JSON in text. There is no `response_format`, because the audio model's page lists only streaming and function calling as supported features, so Go validates strictly instead.

## Known gaps and inconsistencies (as of 2026-11-15)

- The demo has **no authentication**, no retries and no idempotency key. A failed Notion write after a successful extraction loses the note until the user uploads again.
- The upload spinner in `mobile/App.tsx` says "Transcribing and saving to Notion…", but the demo never transcribes: it makes one audio-native call and keeps no transcript. Only the ADR's production design transcribes.
- Quality is untested beyond manual smoke runs (three synthetic recordings and one silent simulator recording). The "2 to 4 seconds per request" figure in `NEXT_STEPS.md` comes from four runs and is not a baseline.

## Commands

```bash
# Backend tests (no credentials needed)
cd backend && go test ./...

# Run the backend (needs backend/.env)
cd backend && go run .

# Try the bundled sample
curl -i -F "audio=@samples/sample-note.wav" http://localhost:8080/notes

# Mobile
cd mobile && npm install && npm test        # tests
cd mobile && npx expo start                  # run; scan the QR code with an iPhone
```
