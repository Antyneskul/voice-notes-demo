package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// Note is the structured result we extract from a voice recording.
type Note struct {
	Summary     string   `json:"summary"`
	ActionItems []string `json:"action_items"`
	Tags        []string `json:"tags"`
}

// Limits are chosen so a valid Note always fits in a single Notion request
// (rich text is capped at 2000 characters per block, 100 blocks per request). They are
// measured in characters the way Notion counts them (see textLen), not in bytes, so notes
// in Cyrillic, CJK or with emoji are not rejected for being "long".
const (
	maxSummaryLen    = 1500
	maxActionItems   = 20
	maxActionItemLen = 500
	maxTags          = 5
	maxTagLen        = 40
)

// ErrInvalidNote means the model answered, but not with a note we trust enough
// to write to Notion. Messages never include model output: it derives from the
// recording and must stay out of logs.
var ErrInvalidNote = errors.New("model returned an invalid note")

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidNote, reason)
}

// ParseNote validates the model's text output. It is strict about shape (all
// three fields present, no extras, sane lengths) and lenient only about
// cosmetics (a markdown fence, tag case and whitespace).
func ParseNote(raw string) (Note, error) {
	raw = stripCodeFence(raw)
	if err := requireExactKeys(raw); err != nil {
		return Note{}, err
	}

	dec := json.NewDecoder(strings.NewReader(raw))
	var n Note
	if err := dec.Decode(&n); err != nil {
		return Note{}, invalid("output is not a JSON object with the expected fields")
	}
	// Anything after the object (even whitespace-separated text) is a failure.
	if _, err := dec.Token(); err != io.EOF {
		return Note{}, invalid("unexpected content after the JSON object")
	}

	n.Summary = strings.TrimSpace(n.Summary)
	if n.Summary == "" {
		return Note{}, invalid("summary is empty")
	}
	if textLen(n.Summary) > maxSummaryLen {
		return Note{}, invalid("summary is too long")
	}

	// A missing or null array decodes to nil; an explicit [] decodes to an
	// empty non-nil slice. Only the latter means "no action items".
	if n.ActionItems == nil {
		return Note{}, invalid("action_items is missing (use [] when there are none)")
	}
	if len(n.ActionItems) > maxActionItems {
		return Note{}, invalid("too many action items")
	}
	for i, item := range n.ActionItems {
		item = strings.TrimSpace(item)
		if item == "" {
			return Note{}, invalid("an action item is blank")
		}
		if textLen(item) > maxActionItemLen {
			return Note{}, invalid("an action item is too long")
		}
		n.ActionItems[i] = item
	}

	if n.Tags == nil {
		return Note{}, invalid("tags is missing (use [] when there are none)")
	}
	tags, err := normalizeTags(n.Tags)
	if err != nil {
		return Note{}, err
	}
	n.Tags = tags

	return n, nil
}

// requireExactKeys checks that the top-level object has exactly the expected
// keys, each spelled exactly and appearing once. encoding/json alone would
// accept "Summary" for "summary" and let a duplicate key silently overwrite an
// earlier one, so output that breaks the field contract could get through.
func requireExactKeys(raw string) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return invalid("output is not a JSON object with the expected fields")
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, isString := tok.(string)
		if err != nil || !isString {
			return invalid("output is not a JSON object with the expected fields")
		}
		if key != "summary" && key != "action_items" && key != "tags" {
			return invalid("output has an unexpected or mis-cased field")
		}
		if seen[key] {
			return invalid("output repeats a field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil { // skip over the value
			return invalid("output is not a JSON object with the expected fields")
		}
	}
	return nil
}

func normalizeTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			return nil, invalid("a tag is blank")
		}
		if textLen(t) > maxTagLen {
			return nil, invalid("a tag is too long")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	// Extra tags are cosmetic: keep the first few instead of failing the note.
	if len(out) > maxTags {
		out = out[:maxTags]
	}
	return out, nil
}

// stripCodeFence removes a surrounding ```json ... ``` fence, which chat models
// sometimes add even when asked for bare JSON.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// textLen is the length Notion applies to its 2000-character limit: UTF-16 code units, like a
// JavaScript string's length. len() would count UTF-8 bytes (2-3 per Cyrillic or CJK character).
func textLen(s string) int { return len(utf16.Encode([]rune(s))) }
