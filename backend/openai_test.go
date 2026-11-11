package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const validNoteJSON = `{"summary":"Met Dana at Acme.","action_items":["Send revised quote by Friday"],"tags":["acme"]}`

// chatReply builds an OpenAI Chat Completions response carrying `content`.
func chatReply(content, finishReason string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": finishReason,
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
	})
	return string(b)
}

func newTestExtractor(url string) *OpenAIExtractor {
	return &OpenAIExtractor{
		BaseURL: url,
		APIKey:  "sk-test",
		Model:   "gpt-audio-1.5",
		HTTP:    http.DefaultClient,
		Timeout: 2 * time.Second,
	}
}

func TestOpenAIExtract_SendsAudioAsTextOnlyChatCompletion(t *testing.T) {
	audio := []byte("RIFF\x00\x00\x00\x00WAVEfake-pcm-data")

	var gotPath, gotAuth, gotType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotType = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, chatReply(validNoteJSON, "stop"))
	}))
	defer srv.Close()

	note, err := newTestExtractor(srv.URL).Extract(context.Background(), audio, "wav")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if note.Summary != "Met Dana at Acme." || len(note.ActionItems) != 1 {
		t.Fatalf("unexpected note: %+v", note)
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if gotBody["model"] != "gpt-audio-1.5" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if mods, _ := gotBody["modalities"].([]any); len(mods) != 1 || mods[0] != "text" {
		t.Errorf("modalities = %v, want [text]", gotBody["modalities"])
	}
	if gotBody["store"] != false {
		t.Errorf("store = %v, want false", gotBody["store"])
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("want system + user messages, got %d", len(msgs))
	}
	system := msgs[0].(map[string]any)
	if system["role"] != "system" {
		t.Errorf("first message role = %v", system["role"])
	}
	prompt, _ := system["content"].(string)
	for _, want := range []string{"summary", "action_items", "tags"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt does not mention %q", want)
		}
	}

	user := msgs[1].(map[string]any)
	var inputAudio map[string]any
	for _, part := range user["content"].([]any) {
		p := part.(map[string]any)
		if p["type"] == "input_audio" {
			inputAudio = p["input_audio"].(map[string]any)
		}
	}
	if inputAudio == nil {
		t.Fatal("user message has no input_audio part")
	}
	if inputAudio["format"] != "wav" {
		t.Errorf("input_audio.format = %v", inputAudio["format"])
	}
	if inputAudio["data"] != base64.StdEncoding.EncodeToString(audio) {
		t.Error("input_audio.data is not the base64 of the uploaded bytes")
	}
}

func TestOpenAIExtract_RejectsInvalidModelOutput(t *testing.T) {
	cases := map[string]string{
		"prose instead of JSON": chatReply("Sure! Here is a summary of the call.", "stop"),
		"missing action_items":  chatReply(`{"summary":"x","tags":[]}`, "stop"),
		"truncated output":      chatReply(validNoteJSON, "length"),
		"no choices":            `{"choices":[]}`,
		"empty content":         chatReply("", "stop"),
		"refusal":               `{"choices":[{"finish_reason":"stop","message":{"content":null,"refusal":"I can't help with that."}}]}`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, reply)
			}))
			defer srv.Close()

			_, err := newTestExtractor(srv.URL).Extract(context.Background(), []byte("audio"), "wav")
			if !errors.Is(err, ErrInvalidNote) {
				t.Fatalf("want ErrInvalidNote, got %v", err)
			}
		})
	}
}

func TestOpenAIExtract_UpstreamErrorKeepsOnlyStatusAndCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Rate limit reached for org-secret-123","type":"requests","code":"rate_limit_exceeded"}}`)
	}))
	defer srv.Close()

	_, err := newTestExtractor(srv.URL).Extract(context.Background(), []byte("audio"), "wav")

	var up *UpstreamError
	if !errors.As(err, &up) {
		t.Fatalf("want *UpstreamError, got %v", err)
	}
	if up.Service != "openai" || up.Status != 429 || up.Code != "rate_limit_exceeded" {
		t.Errorf("unexpected upstream error: %+v", up)
	}
	if strings.Contains(err.Error(), "org-secret-123") {
		t.Errorf("error text leaks the upstream message: %q", err.Error())
	}
}

func TestOpenAIExtract_TimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // net/http only notices a client hang-up once the body is read
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	ex := newTestExtractor(srv.URL)
	ex.Timeout = 50 * time.Millisecond

	_, err := ex.Extract(context.Background(), []byte("audio"), "wav")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

func TestOpenAIExtract_StopsWhenCallerCancels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // net/http only notices a client hang-up once the body is read
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	_, err := newTestExtractor(srv.URL).Extract(ctx, []byte("audio"), "wav")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestExtractionPromptMatchesTagLimit(t *testing.T) {
	want := fmt.Sprintf("up to %d short", maxTags)
	if !strings.Contains(extractionPrompt, want) {
		t.Fatalf("prompt does not say %q; the prompt and ParseNote disagree on the tag limit", want)
	}
}

// A provider (or a proxy configured as OPENAI_BASE_URL) controls the error body.
// Anything that is not a short machine-style token must not survive into logs.
func TestOpenAIExtract_UnrecognisedErrorCodeIsNotPassedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"Dana's card number is 4111-1111-1111-1111"}}`)
	}))
	defer srv.Close()

	_, err := newTestExtractor(srv.URL).Extract(context.Background(), []byte("audio"), "wav")

	var up *UpstreamError
	if !errors.As(err, &up) {
		t.Fatalf("want *UpstreamError, got %v", err)
	}
	if up.Code != "unrecognized" {
		t.Errorf("Code = %q, want %q", up.Code, "unrecognized")
	}
	if strings.Contains(err.Error(), "4111") {
		t.Errorf("error text leaks provider-supplied content: %q", err.Error())
	}
}
