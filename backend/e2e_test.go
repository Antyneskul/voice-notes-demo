package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEndToEnd runs the real router with the real OpenAI and Notion clients,
// pointed at local doubles, so it covers the wiring the unit tests skip:
// upload -> OpenAI request -> validation -> Notion request -> 201.
func TestEndToEnd(t *testing.T) {
	var openaiBody, notionBody map[string]any

	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &openaiBody)
		io.WriteString(w, chatReply(validNoteJSON, "stop"))
	}))
	defer openai.Close()

	notion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &notionBody)
		io.WriteString(w, `{"id":"abc","url":"https://www.notion.so/Voice-note-abc"}`)
	}))
	defer notion.Close()

	cfg := Config{
		OpenAIKey: "sk-test", OpenAIModel: "gpt-audio-1.5", OpenAIBaseURL: openai.URL,
		NotionKey: "ntn-test", NotionParentID: "parent-1", NotionBaseURL: notion.URL,
	}
	h := buildServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Router()

	audio := wavBytes(4096)
	rec := do(h, multipartRequest(t, part{"audio", "note.wav", audio}))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["summary"] != "Met Dana at Acme." || resp["notion_page_url"] != "https://www.notion.so/Voice-note-abc" {
		t.Errorf("unexpected response: %v", resp)
	}

	// The uploaded bytes reached OpenAI, base64-encoded.
	msgs := openaiBody["messages"].([]any)
	parts := msgs[1].(map[string]any)["content"].([]any)
	gotData := parts[1].(map[string]any)["input_audio"].(map[string]any)["data"]
	if gotData != base64.StdEncoding.EncodeToString(audio) {
		t.Error("OpenAI did not receive the uploaded audio")
	}
	// What OpenAI returned reached Notion under the configured parent.
	if notionBody["parent"].(map[string]any)["page_id"] != "parent-1" {
		t.Errorf("notion parent = %v", notionBody["parent"])
	}
	if !bytes.Contains(mustJSON(t, notionBody["children"]), []byte("Met Dana at Acme.")) {
		t.Error("the extracted summary was not written to Notion")
	}
}

func TestEndToEnd_NothingWrittenToNotionWhenModelOutputIsInvalid(t *testing.T) {
	notionCalled := false
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, chatReply(`{"summary":"x"}`, "stop")) // action_items and tags missing
	}))
	defer openai.Close()
	notion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notionCalled = true
	}))
	defer notion.Close()

	cfg := Config{OpenAIKey: "k", OpenAIModel: "m", OpenAIBaseURL: openai.URL, NotionKey: "k", NotionParentID: "p", NotionBaseURL: notion.URL}
	h := buildServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Router()

	rec := do(h, multipartRequest(t, part{"audio", "note.wav", wavBytes(512)}))

	if rec.Code != http.StatusBadGateway || errorCodeOf(t, rec) != "model_output_invalid" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if notionCalled {
		t.Fatal("Notion was called with an unvalidated note")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
