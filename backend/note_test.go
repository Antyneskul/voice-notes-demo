package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseNote_AcceptsValidJSON(t *testing.T) {
	raw := `{"summary":"Met Dana at Acme.","action_items":["Send revised quote by Friday"],"tags":["acme","pricing"]}`

	got, err := ParseNote(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Note{
		Summary:     "Met Dana at Acme.",
		ActionItems: []string{"Send revised quote by Friday"},
		Tags:        []string{"acme", "pricing"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseNote_AllowsEmptyActionItems(t *testing.T) {
	got, err := ParseNote(`{"summary":"Just an FYI.","action_items":[],"tags":[]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ActionItems == nil || len(got.ActionItems) != 0 {
		t.Fatalf("want empty non-nil action items, got %#v", got.ActionItems)
	}
}

func TestParseNote_StripsMarkdownCodeFence(t *testing.T) {
	raw := "```json\n{\"summary\":\"Fenced.\",\"action_items\":[],\"tags\":[]}\n```"

	got, err := ParseNote(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Summary != "Fenced." {
		t.Fatalf("got summary %q", got.Summary)
	}
}

func TestParseNote_NormalizesTags(t *testing.T) {
	got, err := ParseNote(`{"summary":"x","action_items":[],"tags":["  Acme ","ACME","Pricing"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"acme", "pricing"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("got tags %v, want %v", got.Tags, want)
	}
}

func TestParseNote_Rejects(t *testing.T) {
	tooManyItems := `"` + strings.Repeat(`a","`, maxActionItems) + `a"`
	cases := map[string]string{
		"not json":               `I think the customer wants a discount`,
		"empty string":           ``,
		"trailing garbage":       `{"summary":"x","action_items":[],"tags":[]} extra`,
		"missing summary":        `{"action_items":[],"tags":[]}`,
		"blank summary":          `{"summary":"   ","action_items":[],"tags":[]}`,
		"missing action_items":   `{"summary":"x","tags":[]}`,
		"null action_items":      `{"summary":"x","action_items":null,"tags":[]}`,
		"missing tags":           `{"summary":"x","action_items":[]}`,
		"action_items not array": `{"summary":"x","action_items":"call back","tags":[]}`,
		"blank action item":      `{"summary":"x","action_items":["  "],"tags":[]}`,
		"non-string action item": `{"summary":"x","action_items":[1],"tags":[]}`,
		"unknown field":          `{"summary":"x","action_items":[],"tags":[],"sentiment":"happy"}`,
		"summary too long":       `{"summary":"` + strings.Repeat("a", maxSummaryLen+1) + `","action_items":[],"tags":[]}`,
		"action item too long":   `{"summary":"x","action_items":["` + strings.Repeat("a", maxActionItemLen+1) + `"],"tags":[]}`,
		"too many action items":  `{"summary":"x","action_items":[` + tooManyItems + `],"tags":[]}`,
		"blank tag":              `{"summary":"x","action_items":[],"tags":[""]}`,
		"tag too long":           `{"summary":"x","action_items":[],"tags":["` + strings.Repeat("a", maxTagLen+1) + `"]}`,
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseNote(raw)
			if !errors.Is(err, ErrInvalidNote) {
				t.Fatalf("want ErrInvalidNote, got %v", err)
			}
		})
	}
}

// Validation errors end up in logs, so they must describe the problem
// without echoing anything the model (and therefore the recording) said.
func TestParseNote_ErrorDoesNotEchoContent(t *testing.T) {
	secret := "Dana's SSN is 123-45-6789"
	_, err := ParseNote(`{"summary":"` + secret + `","action_items":"oops","tags":[]}`)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "123-45-6789") || strings.Contains(err.Error(), "Dana") {
		t.Fatalf("error leaks note content: %q", err.Error())
	}
}

// encoding/json matches keys case-insensitively and keeps the last duplicate,
// which would let output that breaks the exact-field contract through.
func TestParseNote_RejectsDuplicateAndMiscasedKeys(t *testing.T) {
	cases := map[string]string{
		"duplicate summary":       `{"summary":"first","summary":"second","action_items":[],"tags":[]}`,
		"duplicate action_items":  `{"summary":"x","action_items":["a"],"action_items":[],"tags":[]}`,
		"duplicate tags":          `{"summary":"x","action_items":[],"tags":["a"],"tags":["b"]}`,
		"capitalised summary":     `{"Summary":"x","action_items":[],"tags":[]}`,
		"upper-case action_items": `{"summary":"x","ACTION_ITEMS":[],"tags":[]}`,
		"mixed-case tags":         `{"summary":"x","action_items":[],"Tags":[]}`,
		"both cases present":      `{"summary":"x","Summary":"y","action_items":[],"tags":[]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseNote(raw); !errors.Is(err, ErrInvalidNote) {
				t.Fatalf("want ErrInvalidNote, got %v", err)
			}
		})
	}
}

// The prompt asks for at most five tags, so the parser must not allow more.
func TestParseNote_KeepsAtMostFiveTags(t *testing.T) {
	got, err := ParseNote(`{"summary":"x","action_items":[],"tags":["a","b","c","d","e","f","g"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"a", "b", "c", "d", "e"}; !reflect.DeepEqual(got.Tags, want) {
		t.Fatalf("got tags %v, want %v", got.Tags, want)
	}
}

// Notion's 2000 limit counts characters. Cyrillic or CJK text is 2-3 bytes per character in
// UTF-8, so a limit measured in bytes would reject valid notes after the model call was paid for.
func TestParseNote_LimitsCountCharactersNotBytes(t *testing.T) {
	cyr := func(n int) string { return strings.Repeat("я", n) } // 2 bytes each
	note := func(summary, item, tag string) string {
		return fmt.Sprintf(`{"summary":%q,"action_items":[%q],"tags":[%q]}`, summary, item, tag)
	}

	if _, err := ParseNote(note(cyr(maxSummaryLen), cyr(maxActionItemLen), cyr(maxTagLen))); err != nil {
		t.Fatalf("notes at exactly the character limits must be accepted: %v", err)
	}
	for name, raw := range map[string]string{
		"summary over":     note(cyr(maxSummaryLen+1), "x", "x"),
		"action item over": note("x", cyr(maxActionItemLen+1), "x"),
		"tag over":         note("x", "x", cyr(maxTagLen+1)),
	} {
		if _, err := ParseNote(raw); !errors.Is(err, ErrInvalidNote) {
			t.Errorf("%s: want ErrInvalidNote, got %v", name, err)
		}
	}
}

// Emoji are 2 UTF-16 code units, which is what Notion (like a JavaScript string) counts.
func TestParseNote_CountsUTF16UnitsLikeNotion(t *testing.T) {
	emoji := func(n int) string { return strings.Repeat("😀", n) }
	summary := func(n int) string { return fmt.Sprintf(`{"summary":%q,"action_items":[],"tags":[]}`, emoji(n)) }

	if _, err := ParseNote(summary(maxSummaryLen / 2)); err != nil {
		t.Fatalf("%d emoji are %d UTF-16 units, exactly the limit: %v", maxSummaryLen/2, maxSummaryLen, err)
	}
	if _, err := ParseNote(summary(maxSummaryLen/2 + 1)); !errors.Is(err, ErrInvalidNote) {
		t.Fatalf("one emoji over the limit must be rejected, got %v", err)
	}
}
