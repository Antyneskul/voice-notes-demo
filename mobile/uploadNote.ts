// The explicit .ts lets Node's test runner load this file too (see tsconfig: allowImportingTsExtensions).
import { asNoteResult, type NoteResult } from './noteResult.ts';

type ReplyLike = { ok: boolean; status: number; json(): Promise<unknown> };

const TIMED_OUT = 'The server took too long to answer. Try again.';

// Sends the recording and returns the note, or throws an Error whose message is meant to be
// shown to the user. `fetchFn` is a parameter so the failure paths can be tested.
//
// expo/fetch reports every network failure, and an abort, as a plain Error (its FetchError),
// never a TypeError or AbortError, so those names can't be used to tell the cases apart.
// Whatever fetch throws means "no answer"; whether it was our own timeout is read from the signal.
export async function uploadNote({
  baseUrl,
  body,
  fetchFn,
  timeoutMs,
}: {
  baseUrl: string;
  body: FormData;
  fetchFn: (url: string, init: { method: 'POST'; body: FormData; signal: AbortSignal }) => Promise<ReplyLike>;
  timeoutMs: number;
}): Promise<NoteResult> {
  const url = baseUrl.trim().replace(/\/+$/, '');
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    let reply: ReplyLike;
    try {
      // No Content-Type header: fetch adds it, including the multipart boundary.
      reply = await fetchFn(`${url}/notes`, { method: 'POST', body, signal: controller.signal });
    } catch {
      throw new Error(
        controller.signal.aborted
          ? TIMED_OUT
          : `Could not reach ${url}. Is the backend running, and is this phone on the same Wi-Fi network?`
      );
    }

    let data: unknown = null;
    try {
      data = await reply.json();
    } catch {
      if (controller.signal.aborted) throw new Error(TIMED_OUT); // the body stalled
    }

    if (!reply.ok) {
      const message = (data as { error?: { message?: unknown } } | null)?.error?.message;
      throw new Error(typeof message === 'string' ? message : `The server answered HTTP ${reply.status}.`);
    }
    const note = asNoteResult(data);
    if (!note) throw new Error('Unexpected response from the server. Is the backend URL correct?');
    return note;
  } finally {
    clearTimeout(timer);
  }
}
