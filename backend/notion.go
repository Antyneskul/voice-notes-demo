package main

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// notionVersion pins the API behaviour we were written against.
const notionVersion = "2026-03-11"

// NotionExporter creates one child page per note under a fixed parent page.
type NotionExporter struct {
	BaseURL  string // e.g. https://api.notion.com
	Token    string // personal access token
	ParentID string
	HTTP     *http.Client
	Timeout  time.Duration
}

// CreatePage writes the note and returns the URL of the new page. It only
// returns a URL once Notion has confirmed the write with a 2xx response.
func (n *NotionExporter) CreatePage(ctx context.Context, note Note, at time.Time) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()

	body := map[string]any{
		"parent": map[string]any{"page_id": n.ParentID},
		// For a page under another page, "title" is the only property Notion allows.
		"properties": map[string]any{
			"title": map[string]any{"title": richText("Voice note " + at.UTC().Format("2006-01-02 15:04 MST"))},
		},
		"children": noteBlocks(note),
	}

	var resp struct {
		URL string `json:"url"`
	}
	headers := map[string]string{
		"Authorization":  "Bearer " + n.Token,
		"Notion-Version": notionVersion,
	}
	if err := postJSON(ctx, n.HTTP, "notion", n.BaseURL+"/v1/pages", headers, body, &resp); err != nil {
		return "", err
	}
	if resp.URL == "" {
		return "", &UpstreamError{Service: "notion", Status: http.StatusOK, Code: "invalid_response"}
	}
	return resp.URL, nil
}

// noteBlocks lays the note out as Summary / Action items (checkboxes) / Tags.
// ParseNote's length limits keep every block inside Notion's 2000-char cap.
func noteBlocks(note Note) []any {
	blocks := []any{
		textBlock("heading_2", "Summary"),
		textBlock("paragraph", note.Summary),
		textBlock("heading_2", "Action items"),
	}
	if len(note.ActionItems) == 0 {
		blocks = append(blocks, textBlock("paragraph", "None mentioned in the recording."))
	}
	for _, item := range note.ActionItems {
		blocks = append(blocks, map[string]any{
			"object": "block",
			"type":   "to_do",
			"to_do":  map[string]any{"rich_text": richText(item), "checked": false},
		})
	}
	if len(note.Tags) > 0 {
		blocks = append(blocks,
			textBlock("heading_2", "Tags"),
			textBlock("paragraph", strings.Join(note.Tags, ", ")),
		)
	}
	return blocks
}

func richText(s string) []any {
	return []any{map[string]any{"type": "text", "text": map[string]any{"content": s}}}
}

func textBlock(kind, s string) map[string]any {
	return map[string]any{"object": "block", "type": kind, kind: map[string]any{"rich_text": richText(s)}}
}
