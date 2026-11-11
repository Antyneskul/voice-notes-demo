import test from 'node:test';
import assert from 'node:assert/strict';
import { uploadNote } from './uploadNote.ts';

const form = 'form-data' as unknown as FormData;
const note = { summary: 'Met Dana.', action_items: ['Send quote'], tags: ['acme'], notion_page_url: 'https://www.notion.so/x' };

type Init = { method: string; body: unknown; signal: AbortSignal };
const reply = (status: number, body: unknown) => async () => ({
  ok: status >= 200 && status < 300,
  status,
  json: async () => {
    if (body === undefined) throw new SyntaxError('Unexpected end of JSON input');
    return body;
  },
});

// expo/fetch reports network failures, and aborts, as a plain Error (its FetchError), not a TypeError.
class FetchError extends Error {
  constructor(message: string) {
    super(`fetch failed: ${message}`);
  }
}
const failsWith = (e: Error) => async () => {
  throw e;
};
// A fetch that never answers until the caller aborts it, like a stalled connection.
const hangsUntilAborted = async (_url: string, init: Init) =>
  new Promise<never>((_, reject) => init.signal.addEventListener('abort', () => reject(new FetchError('The operation was aborted.'))));

const upload = (fetchFn: (url: string, init: Init) => Promise<unknown>, extra: { baseUrl?: string; timeoutMs?: number } = {}) =>
  uploadNote({ baseUrl: 'http://10.0.0.5:8080', body: form, timeoutMs: 1000, ...extra, fetchFn: fetchFn as never });

test('returns the parsed note on 201', async () => {
  assert.deepEqual(await upload(reply(201, note)), note);
});

test('posts the form to <baseUrl>/notes, trimming whitespace and trailing slashes, with an abort signal', async () => {
  let seen: { url: string; init: Init } | undefined;
  await upload(async (url, init) => { seen = { url, init }; return reply(201, note)(); }, { baseUrl: '  http://10.0.0.5:8080//  ' });
  assert.equal(seen?.url, 'http://10.0.0.5:8080/notes');
  assert.equal(seen?.init.method, 'POST');
  assert.equal(seen?.init.body, form);
  assert.ok(seen?.init.signal instanceof AbortSignal);
});

test("shows the server's own error message", async () => {
  await assert.rejects(upload(reply(415, { error: { code: 'unsupported_audio', message: 'Unsupported audio.' } })), { message: 'Unsupported audio.' });
});

test('names the HTTP status when the error body has no message', async () => {
  await assert.rejects(upload(reply(502, undefined)), { message: 'The server answered HTTP 502.' });
});

test('says so when a 2xx reply is not a note (wrong URL or incompatible backend)', async () => {
  await assert.rejects(upload(reply(200, { hello: 'world' })), /Unexpected response from the server/);
  await assert.rejects(upload(reply(200, undefined)), /Unexpected response from the server/);
});

test('an expo/fetch FetchError (a plain Error) is reported as "could not reach", with the URL', async () => {
  await assert.rejects(upload(failsWith(new FetchError('Connection refused'))), {
    message: 'Could not reach http://10.0.0.5:8080. Is the backend running, and is this phone on the same Wi-Fi network?',
  });
});

test('a TypeError from another fetch implementation is reported the same way', async () => {
  await assert.rejects(upload(failsWith(new TypeError('Network request failed'))), /^Error: Could not reach http:\/\/10\.0\.0\.5:8080/);
});

test('a stalled connection times out with its own message, even though fetch rejects with a plain Error', async () => {
  await assert.rejects(upload(hangsUntilAborted, { timeoutMs: 30 }), { message: 'The server took too long to answer. Try again.' });
});

test('a reply whose body stalls also times out', async () => {
  const stalledBody = async (_url: string, init: Init) => ({
    ok: true,
    status: 201,
    json: () => new Promise<never>((_, reject) => init.signal.addEventListener('abort', () => reject(new FetchError('aborted')))),
  });
  await assert.rejects(upload(stalledBody, { timeoutMs: 30 }), { message: 'The server took too long to answer. Try again.' });
});
