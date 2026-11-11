# Next steps

Measure extraction quality, reliability and cost on the demo first. Before a production pilot, ship the authenticated upload and processing flow, rep approval, and the first CRM adapter. The [architecture decision record](ADR_001_Architecture.md) describes the proposed system and owns its design; this file adds the work and its order. Sections are in priority order, and the last one is later work.

## Evals

- Build a labeled dataset covering clear speech, noise, accents, multiple languages, silence, unrelated chatter, ambiguous owners and dates, customer versus rep commitments, and spoken instructions aimed at the model.
- Score summary faithfulness and relevance, action-item precision and recall, owner and deadline accuracy, and schema failure rates. For the two-stage pipeline, distinguish transcription errors from extraction errors.
- Compare the demo's audio-native call with speech-to-text plus text extraction on the same held-out recordings. Weigh the transcript's value for review and debugging against the added latency and cost.
- Run the same held-out cases on prompt, model and routing changes. Record latency and cost alongside quality. Set release thresholds once the baseline is measured.
- So far the demo has only had manual smoke checks: three synthetic recordings (including the bundled sample) and one silent simulator recording. They show the pipeline works end to end and say nothing about quality at scale.

## Content validation and relevance

- Keep Go validation for field types and limits. Separately check that each material summary claim is supported by the transcript and relevant to the customer visit. Exclude unrelated personal chatter and preserve the owner and deadline of each stated commitment.
- If the recording contains no usable customer-visit details, tell the rep and leave the action list empty. Flag unsupported or ambiguous claims in the draft and show the relevant transcript excerpt when available. Require rep approval before export.
- Test a separate transcript verification call if unsupported claims regularly survive the extraction prompt. Measure the errors it catches, correct claims it flags, and added latency and cost.

## Demo hardening

- Retry once with backoff on a 429 or 5xx, and once when the model's output fails validation. Today either failure goes straight back to the user.
- Add an idempotency key so a retried upload cannot create a second Notion page.
- Add request IDs, per-stage latency and a limit on concurrent uploads, since each request holds the audio and its base64 copy in memory.
- Add authentication to the endpoint, which is open to anyone on the same network today.
- Use schema-enforced output once the audio model supports it. Its model page lists only streaming and function calling as supported features, so the demo asks for JSON in the prompt and validates strictly in Go.

## Security

- Authenticate users over HTTPS and authorize each note, upload, job and CRM connection within its tenant. Use short-lived, note-specific upload URLs and verify the uploaded object before queueing it.
- Keep provider keys and CRM credentials server-side in a secrets manager. Scope access for the API, worker, tenant admins and support staff. Audit credential changes and manual retries.
- Set audio and transcript retention and deletion policies. Review provider data handling and retention terms. Apply upload size, duration and concurrency limits. Bound retries and use CRM idempotency or reconciliation to avoid duplicate writes.

## Deployment

- Build one Go container image with separate API and worker entry points. Deploy them as separate ECS Fargate services with independent configuration and scaling. Use RDS PostgreSQL for state and leased jobs, and private S3 storage for audio.
- Set up staging with separate credentials, storage and a test CRM destination. Automate image builds, backward-compatible database migrations and deployment. Check that worker restarts release or expire job leases and that rollback leaves queued jobs processable.
- Roll out in order: resumable mobile upload and durable processing, rep review and approval, then CRM export. Smoke-test the path from an offline recording through a successful export. Verify recording, interruption and upload recovery on iOS and Android, including whether the speech-to-text provider accepts the recorded format or needs transcoding.

## Observability and tracing

- Correlate API requests, worker attempts and CRM exports with opaque identifiers. Track upload and stage latency, lease expirations, retry rates and validation failures, in addition to the monitoring and alerts listed under [Production operations](ADR_001_Architecture.md#production-operations).
- Record one span per speech-to-text, extraction or verification attempt. Include the trace ID, job stage, attempt number, provider, model version, prompt version, duration and validation outcome.
- Capture provider-reported input, output, cached and audio token counts where available. Record audio duration and the billing unit for duration-priced calls. Preserve missing usage as unknown and include retries in attribution.
- Use Langfuse for model traces if it meets the team's data handling requirements. Keep raw audio, transcripts, prompts containing note content, and model responses out of traces by default.
- Treat a CRM timeout with an uncertain outcome as a reconciliation case. Use staging measurements to set latency and reliability targets. In four manual demo runs a request took roughly two to four seconds, which is too few runs, and too varied in length, to use as a baseline: measure latency by audio duration first.

## Pilot acceptance criteria

- An offline recording survives an app restart and uploads when the rep reopens the app with connectivity.
- After upload confirmation, a queued note reaches review with the app closed, even if the worker restarts.
- The CRM receives the exact version the rep approved. If a write times out after it may have succeeded, support can reconcile the outcome without creating a duplicate.
- On representative pilot recordings, extraction quality, end-to-end latency and cost per processed note meet the targets agreed before rollout.

## Cost and model routing (later)

- Calculate cost per audio minute, processed note and successfully exported note. Attribute all attempts, including retries and verification, to the original note. Keep speech-to-text, extraction and verification costs visible separately.
- Reconcile estimated costs with provider invoices. Compare short and long recordings and monitor spending by tenant. Set duration and retry budgets and alert on unexpected spend.
- Start with one baseline model per stage. Compare candidate routes in evals before enabling them in production. A lower-cost text model can handle routine extraction if it meets the same quality thresholds.
- Test escalation to a stronger model for a failed structured response or a case with insufficient evidence for a material claim. Use a separate fallback for provider timeouts and rate limits. Cap attempts and keep uncertain content visible for rep review.
- Trace the selected route and the reason for each escalation or fallback, as described under Observability and tracing. Keep routing changes only when they meet quality thresholds and improve cost or latency on the same held-out recordings.
