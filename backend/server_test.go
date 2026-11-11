package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// ---- fakes for the two interfaces the handler depends on ----

type fakeExtractor struct {
	note          Note
	err           error
	waitForCancel bool // block until the request context is cancelled

	calls  int
	audio  []byte
	format string
}

func (f *fakeExtractor) Extract(ctx context.Context, audio []byte, format string) (Note, error) {
	f.calls++
	f.audio, f.format = audio, format
	if f.waitForCancel {
		<-ctx.Done()
		return Note{}, ctx.Err()
	}
	return f.note, f.err
}

type fakeExporter struct {
	url      string
	err      error
	panicMsg string

	calls int
	note  Note
}

func (f *fakeExporter) CreatePage(ctx context.Context, note Note, at time.Time) (string, error) {
	f.calls++
	f.note = note
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	return f.url, f.err
}

// ---- helpers ----

const sensitiveSummary = "Dana's card number is 4111-1111-1111-1111"

func goodNote() Note {
	return Note{Summary: sensitiveSummary, ActionItems: []string{"Send revised quote by Friday"}, Tags: []string{"acme"}}
}

func newTestServer(ex Extractor, exp Exporter) (http.Handler, *bytes.Buffer) {
	var logs bytes.Buffer
	s := &Server{Extractor: ex, Exporter: exp, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	return s.Router(), &logs
}

// wavBytes returns a valid WAV of exactly `size` bytes (size must be even and at least 46).
func wavBytes(size int) []byte {
	return buildWAV(false, size-44)
}

type part struct {
	field    string
	filename string // empty means a plain form field
	data     []byte
}

func multipartRequest(t *testing.T, parts ...part) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, p := range parts {
		var dst io.Writer
		var err error
		if p.filename == "" {
			dst, err = w.CreateFormField(p.field)
		} else {
			dst, err = w.CreateFormFile(p.field, p.filename)
		}
		if err != nil {
			t.Fatal(err)
		}
		dst.Write(p.data)
	}
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/notes", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func do(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the JSON error envelope: %q", rec.Body.String())
	}
	if body.Error.Message == "" {
		t.Errorf("error response has no message: %q", rec.Body.String())
	}
	return body.Error.Code
}

// ---- tests ----

func TestCreateNote_Success(t *testing.T) {
	ex := &fakeExtractor{note: goodNote()}
	exp := &fakeExporter{url: "https://www.notion.so/Voice-note-abc"}
	h, _ := newTestServer(ex, exp)
	audio := wavBytes(2048)

	rec := do(h, multipartRequest(t, part{"audio", "note.wav", audio}))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	want := map[string]any{
		"summary":         sensitiveSummary,
		"action_items":    []any{"Send revised quote by Friday"},
		"tags":            []any{"acme"},
		"notion_page_url": "https://www.notion.so/Voice-note-abc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body = %v\nwant   %v", got, want)
	}
	if !bytes.Equal(ex.audio, audio) || ex.format != "wav" {
		t.Errorf("extractor got format %q and %d bytes", ex.format, len(ex.audio))
	}
	if !reflect.DeepEqual(exp.note, goodNote()) {
		t.Errorf("exporter got %+v", exp.note)
	}
}

func TestCreateNote_AcceptsMP3(t *testing.T) {
	ex := &fakeExtractor{note: goodNote()}
	h, _ := newTestServer(ex, &fakeExporter{url: "https://www.notion.so/x"})
	mp3 := id3MP3(200, false, layer3Frame)

	rec := do(h, multipartRequest(t, part{"audio", "note.mp3", mp3}))

	if rec.Code != http.StatusCreated || ex.format != "mp3" {
		t.Fatalf("status = %d, format = %q", rec.Code, ex.format)
	}
}

func TestCreateNote_BadUploads(t *testing.T) {
	m4a := append([]byte("\x00\x00\x00\x20ftypM4A "), make([]byte, 100)...)

	cases := []struct {
		name     string
		req      func(t *testing.T) *http.Request
		wantCode int
		wantErr  string
	}{
		{"no audio field", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"other", "note.wav", wavBytes(100)})
		}, 400, "missing_audio"},
		{"audio sent as plain field", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "", wavBytes(100)})
		}, 400, "missing_audio"},
		{"two audio files", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "a.wav", wavBytes(100)}, part{"audio", "b.wav", wavBytes(100)})
		}, 400, "invalid_upload"},
		{"audio plus a file in another field", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "a.wav", wavBytes(100)}, part{"notes", "n.txt", []byte("hi")})
		}, 400, "invalid_upload"},
		{"truncated wav (declared size larger than the file)", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "note.wav", truncatedWAV()})
		}, 415, "unsupported_audio"},
		{"not multipart", func(t *testing.T) *http.Request {
			req := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"audio":"x"}`))
			req.Header.Set("Content-Type", "application/json")
			return req
		}, 400, "invalid_upload"},
		{"m4a named .wav is rejected by content, not by name", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "note.wav", m4a})
		}, 415, "unsupported_audio"},
		{"empty file", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "note.wav", nil})
		}, 415, "unsupported_audio"},
		{"file over 10 MiB", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"audio", "note.wav", wavBytes(maxAudioBytes + 2)})
		}, 413, "too_large"},
		{"total request over the cap via a big extra field", func(t *testing.T) *http.Request {
			return multipartRequest(t, part{"junk", "", make([]byte, maxRequestBytes)}, part{"audio", "note.wav", wavBytes(100)})
		}, 413, "too_large"},
		{"total request over the cap with no Content-Length", func(t *testing.T) *http.Request {
			req := multipartRequest(t, part{"junk", "", make([]byte, maxRequestBytes)}, part{"audio", "note.wav", wavBytes(100)})
			req.ContentLength = -1 // chunked upload: only the body reader can enforce the cap
			return req
		}, 413, "too_large"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex, exp := &fakeExtractor{note: goodNote()}, &fakeExporter{url: "u"}
			h, _ := newTestServer(ex, exp)

			rec := do(h, tc.req(t))

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.wantCode, rec.Body)
			}
			if got := errorCodeOf(t, rec); got != tc.wantErr {
				t.Errorf("error code = %q, want %q", got, tc.wantErr)
			}
			if ex.calls != 0 || exp.calls != 0 {
				t.Errorf("upstreams were called for a rejected upload (openai=%d notion=%d)", ex.calls, exp.calls)
			}
		})
	}
}

func TestCreateNote_UpstreamFailures(t *testing.T) {
	cases := []struct {
		name       string
		ex         *fakeExtractor
		exp        *fakeExporter
		wantCode   int
		wantErr    string
		notionCall int
	}{
		{"model output invalid", &fakeExtractor{err: invalid("summary is empty")}, &fakeExporter{}, 502, "model_output_invalid", 0},
		{"openai rejects the request", &fakeExtractor{err: &UpstreamError{Service: "openai", Status: 401, Code: "invalid_api_key"}}, &fakeExporter{}, 502, "openai_failed", 0},
		{"openai unreachable", &fakeExtractor{err: errors.New("dial tcp: connection refused")}, &fakeExporter{}, 502, "openai_failed", 0},
		{"openai times out", &fakeExtractor{err: fmt.Errorf("openai request failed: %w", context.DeadlineExceeded)}, &fakeExporter{}, 504, "openai_timeout", 0},
		{"notion rejects the request", &fakeExtractor{note: goodNote()}, &fakeExporter{err: &UpstreamError{Service: "notion", Status: 404, Code: "object_not_found"}}, 502, "notion_failed", 1},
		{"notion times out", &fakeExtractor{note: goodNote()}, &fakeExporter{err: fmt.Errorf("notion request failed: %w", context.DeadlineExceeded)}, 504, "notion_timeout", 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestServer(tc.ex, tc.exp)

			rec := do(h, multipartRequest(t, part{"audio", "note.wav", wavBytes(512)}))

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.wantCode, rec.Body)
			}
			if got := errorCodeOf(t, rec); got != tc.wantErr {
				t.Errorf("error code = %q, want %q", got, tc.wantErr)
			}
			if tc.exp.calls != tc.notionCall {
				t.Errorf("notion calls = %d, want %d", tc.exp.calls, tc.notionCall)
			}
		})
	}
}

func TestCreateNote_StopsWhenClientDisconnects(t *testing.T) {
	ex := &fakeExtractor{waitForCancel: true}
	exp := &fakeExporter{}
	h, _ := newTestServer(ex, exp)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	req := multipartRequest(t, part{"audio", "note.wav", wavBytes(512)}).WithContext(ctx)

	rec := do(h, req)

	if rec.Code != 499 {
		t.Fatalf("status = %d, want 499", rec.Code)
	}
	if exp.calls != 0 {
		t.Error("notion was called after the client went away")
	}
}

func TestCreateNote_LogsNeverContainNoteContentOrAudio(t *testing.T) {
	audio := wavBytes(2048)
	copy(audio[100:], "SECRET-AUDIO-BYTES")

	cases := map[string]struct {
		ex  *fakeExtractor
		exp *fakeExporter
	}{
		"success":              {&fakeExtractor{note: goodNote()}, &fakeExporter{url: "https://www.notion.so/x"}},
		"model output invalid": {&fakeExtractor{err: invalid("summary is empty")}, &fakeExporter{}},
		"notion failure":       {&fakeExtractor{note: goodNote()}, &fakeExporter{err: &UpstreamError{Service: "notion", Status: 500, Code: "internal_server_error"}}},
		"handler panics":       {&fakeExtractor{note: goodNote()}, &fakeExporter{panicMsg: "boom: " + sensitiveSummary}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, logs := newTestServer(tc.ex, tc.exp)

			do(h, multipartRequest(t, part{"audio", "note.wav", audio}))

			out := logs.String()
			if out == "" {
				t.Fatal("expected some log output")
			}
			for _, secret := range []string{"4111", "Dana", "SECRET-AUDIO-BYTES", "Friday"} {
				if strings.Contains(out, secret) {
					t.Errorf("logs contain %q:\n%s", secret, out)
				}
			}
		})
	}
}

func TestCreateNote_PanicBecomesGeneric500(t *testing.T) {
	h, _ := newTestServer(&fakeExtractor{note: goodNote()}, &fakeExporter{panicMsg: "boom: " + sensitiveSummary})

	rec := do(h, multipartRequest(t, part{"audio", "note.wav", wavBytes(512)}))

	if rec.Code != 500 || errorCodeOf(t, rec) != "internal_error" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "4111") {
		t.Error("response leaks the panic value")
	}
}

func TestHealthz(t *testing.T) {
	h, _ := newTestServer(&fakeExtractor{}, &fakeExporter{})

	rec := do(h, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

type extractorFunc func(ctx context.Context, audio []byte, format string) (Note, error)

func (f extractorFunc) Extract(ctx context.Context, audio []byte, format string) (Note, error) {
	return f(ctx, audio, format)
}

// The unit test above cancels a context by hand. This one drops a real connection mid-request.
func TestCreateNote_CancelsExtractionWhenClientDisconnectsOverARealSocket(t *testing.T) {
	cancelled := make(chan error, 1)
	h, _ := newTestServer(extractorFunc(func(ctx context.Context, _ []byte, _ string) (Note, error) {
		<-ctx.Done()
		cancelled <- ctx.Err()
		return Note{}, ctx.Err()
	}), &fakeExporter{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	built := multipartRequest(t, part{"audio", "note.wav", wavBytes(512)})
	body, _ := io.ReadAll(built.Body)
	ctx, cancelClient := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/notes", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", built.Header.Get("Content-Type"))

	go http.DefaultClient.Do(req) // blocks until the client gives up
	time.AfterFunc(150*time.Millisecond, cancelClient)

	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context ended with %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler's context was not cancelled after the client disconnected")
	}
}
