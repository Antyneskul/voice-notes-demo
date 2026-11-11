package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 11, 11, 14, 40, 0, 0, time.UTC)

func newTestExporter(url string) *NotionExporter {
	return &NotionExporter{
		BaseURL:  url,
		Token:    "ntn-test",
		ParentID: "parent-page-id",
		HTTP:     http.DefaultClient,
		Timeout:  2 * time.Second,
	}
}

// blockSummary flattens Notion blocks into "type: text" strings for easy assertions.
func blockSummary(t *testing.T, body map[string]any) []string {
	t.Helper()
	var out []string
	for _, b := range body["children"].([]any) {
		blk := b.(map[string]any)
		kind := blk["type"].(string)
		rich := blk[kind].(map[string]any)["rich_text"].([]any)
		text := rich[0].(map[string]any)["text"].(map[string]any)["content"].(string)
		out = append(out, kind+": "+text)
	}
	return out
}

func TestNotionCreatePage_SendsChildPageUnderParent(t *testing.T) {
	var gotPath, gotAuth, gotVersion string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotVersion = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Notion-Version")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"object":"page","id":"abc","url":"https://www.notion.so/Voice-note-abc"}`)
	}))
	defer srv.Close()

	note := Note{
		Summary:     "Met Dana at Acme.",
		ActionItems: []string{"Send revised quote by Friday", "Book security review"},
		Tags:        []string{"acme", "pricing"},
	}
	url, err := newTestExporter(srv.URL).CreatePage(context.Background(), note, fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://www.notion.so/Voice-note-abc" {
		t.Errorf("url = %q", url)
	}

	if gotPath != "/v1/pages" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer ntn-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotVersion != notionVersion {
		t.Errorf("Notion-Version = %q, want %q", gotVersion, notionVersion)
	}

	parent := gotBody["parent"].(map[string]any)
	if parent["page_id"] != "parent-page-id" {
		t.Errorf("parent = %v", parent)
	}
	title := gotBody["properties"].(map[string]any)["title"].(map[string]any)["title"].([]any)[0].(map[string]any)["text"].(map[string]any)["content"].(string)
	if title != "Voice note 2026-11-11 14:40 UTC" {
		t.Errorf("title = %q", title)
	}

	want := []string{
		"heading_2: Summary",
		"paragraph: Met Dana at Acme.",
		"heading_2: Action items",
		"to_do: Send revised quote by Friday",
		"to_do: Book security review",
		"heading_2: Tags",
		"paragraph: acme, pricing",
	}
	if got := blockSummary(t, gotBody); !reflect.DeepEqual(got, want) {
		t.Errorf("blocks:\n got %q\nwant %q", got, want)
	}
}

func TestNotionCreatePage_NoActionItemsAndNoTags(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"url":"https://www.notion.so/x"}`)
	}))
	defer srv.Close()

	note := Note{Summary: "Just an FYI.", ActionItems: []string{}, Tags: []string{}}
	if _, err := newTestExporter(srv.URL).CreatePage(context.Background(), note, fixedTime); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"heading_2: Summary",
		"paragraph: Just an FYI.",
		"heading_2: Action items",
		"paragraph: None mentioned in the recording.",
	}
	if got := blockSummary(t, gotBody); !reflect.DeepEqual(got, want) {
		t.Errorf("blocks:\n got %q\nwant %q", got, want)
	}
}

func TestNotionCreatePage_UpstreamErrorKeepsOnlyStatusAndCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"object":"error","status":404,"code":"object_not_found","message":"Could not find page with ID: secret-page-title"}`)
	}))
	defer srv.Close()

	_, err := newTestExporter(srv.URL).CreatePage(context.Background(), Note{Summary: "x", ActionItems: []string{}, Tags: []string{}}, fixedTime)

	var up *UpstreamError
	if !errors.As(err, &up) {
		t.Fatalf("want *UpstreamError, got %v", err)
	}
	if up.Service != "notion" || up.Status != 404 || up.Code != "object_not_found" {
		t.Errorf("unexpected upstream error: %+v", up)
	}
	if strings.Contains(err.Error(), "secret-page-title") {
		t.Errorf("error text leaks the upstream message: %q", err.Error())
	}
}

func TestNotionCreatePage_RejectsReplyWithoutURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"page","id":"abc"}`)
	}))
	defer srv.Close()

	_, err := newTestExporter(srv.URL).CreatePage(context.Background(), Note{Summary: "x", ActionItems: []string{}, Tags: []string{}}, fixedTime)

	var up *UpstreamError
	if !errors.As(err, &up) || up.Code != "invalid_response" {
		t.Fatalf("want invalid_response upstream error, got %v", err)
	}
}

func TestNotionCreatePage_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	ex := newTestExporter(srv.URL)
	ex.Timeout = 50 * time.Millisecond

	_, err := ex.CreatePage(context.Background(), Note{Summary: "x", ActionItems: []string{}, Tags: []string{}}, fixedTime)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}
