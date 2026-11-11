import test from 'node:test';
import assert from 'node:assert/strict';
import { asNoteResult } from './noteResult.ts';

const valid = {
  summary: 'Met Dana at Acme.',
  action_items: ['Send revised quote by Friday'],
  tags: ['acme'],
  notion_page_url: 'https://www.notion.so/Voice-note-abc',
};

test('accepts a well-formed result', () => {
  assert.deepEqual(asNoteResult(valid), valid);
});

test('accepts empty action_items and tags', () => {
  const r = asNoteResult({ ...valid, action_items: [], tags: [] });
  assert.deepEqual(r?.action_items, []);
  assert.deepEqual(r?.tags, []);
});

test('drops fields the app does not know about', () => {
  assert.deepEqual(asNoteResult({ ...valid, extra: 'x' }), valid);
});

test('rejects bodies that are not objects', () => {
  for (const body of [null, undefined, 'ok', 42, true, [valid]]) {
    assert.equal(asNoteResult(body), null, `body: ${JSON.stringify(body)}`);
  }
});

test('rejects a result with a missing or wrongly typed field', () => {
  const bad: Record<string, unknown>[] = [
    { ...valid, summary: undefined },
    { ...valid, summary: 7 },
    { ...valid, action_items: undefined },
    { ...valid, action_items: null },
    { ...valid, action_items: 'call back' },
    { ...valid, action_items: [1, 2] },
    { ...valid, tags: undefined },
    { ...valid, tags: { 0: 'a' } },
    { ...valid, tags: ['ok', null] },
    { ...valid, notion_page_url: undefined },
    { ...valid, notion_page_url: 42 },
  ];
  for (const body of bad) {
    assert.equal(asNoteResult(body), null, `body: ${JSON.stringify(body)}`);
  }
});

test('rejects a page URL that is not http(s), since the app opens it', () => {
  for (const url of ['javascript:alert(1)', 'file:///etc/passwd', 'notion://page/123', '//evil.example', 'www.notion.so/x', '']) {
    assert.equal(asNoteResult({ ...valid, notion_page_url: url }), null, `url: ${url}`);
  }
});
