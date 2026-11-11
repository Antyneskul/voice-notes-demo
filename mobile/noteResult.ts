export type NoteResult = {
  summary: string;
  action_items: string[];
  tags: string[];
  notion_page_url: string;
};

const isStringArray = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === 'string');

// Narrows an untrusted response body to a NoteResult, or returns null. A 2xx
// from the wrong URL (or an incompatible backend) must not crash the result
// screen, and the page URL is opened with Linking, so it must be http(s).
export function asNoteResult(body: unknown): NoteResult | null {
  if (typeof body !== 'object' || body === null || Array.isArray(body)) return null;
  const b = body as Record<string, unknown>;
  if (
    typeof b.summary !== 'string' ||
    !isStringArray(b.action_items) ||
    !isStringArray(b.tags) ||
    typeof b.notion_page_url !== 'string' ||
    !/^https?:\/\//i.test(b.notion_page_url)
  ) {
    return null;
  }
  return { summary: b.summary, action_items: b.action_items, tags: b.tags, notion_page_url: b.notion_page_url };
}
