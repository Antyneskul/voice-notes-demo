package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxAudioBytes = 10 << 20 // 10 MiB per file
	// The whole request may be a little bigger than the file: multipart adds
	// boundaries and headers. This cap bounds everything the client can send.
	maxRequestBytes = maxAudioBytes + 1<<20
)

// Extractor and Exporter are the two things the handler needs. Declaring them
// as interfaces (Go interfaces are satisfied implicitly) lets tests swap in
// fakes without a mocking library.
type Extractor interface {
	Extract(ctx context.Context, audio []byte, format string) (Note, error)
}

type Exporter interface {
	CreatePage(ctx context.Context, note Note, at time.Time) (pageURL string, err error)
}

type Server struct {
	Extractor Extractor
	Exporter  Exporter
	Log       *slog.Logger
}

// Router wires middleware and routes. Logging is limited to method, path,
// status and duration: never headers, bodies, audio or note content.
func (s *Server) Router() *gin.Engine {
	r := gin.New()
	// Keep uploads in memory: with the default, Go spills big files to temp
	// files on disk, and we'd rather not leave recordings there.
	r.MaxMultipartMemory = maxRequestBytes
	r.Use(s.accessLog(), s.recoverPanic())

	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.POST("/notes", s.createNote)
	return r
}

// createNote is the whole request path:
//
//	upload -> validate -> OpenAI (audio -> Note) -> Notion (Note -> page) -> 201
func (s *Server) createNote(c *gin.Context) {
	// 1. Bound what the client can send, before reading anything.
	if c.Request.ContentLength > maxRequestBytes {
		respondError(c, http.StatusRequestEntityTooLarge, "too_large", "Request is too large (limit 10 MiB of audio).")
		return
	}
	// MaxBytesReader also covers chunked uploads that declare no length.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)

	// 2. Parse the multipart body and require exactly one file in `audio`.
	form, err := c.MultipartForm()
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			respondError(c, http.StatusRequestEntityTooLarge, "too_large", "Request is too large (limit 10 MiB of audio).")
		} else {
			respondError(c, http.StatusBadRequest, "invalid_upload", "Send multipart/form-data with one file in the 'audio' field.")
		}
		return
	}
	if len(form.File) > 1 {
		respondError(c, http.StatusBadRequest, "invalid_upload", "Send exactly one file, in the 'audio' field.")
		return
	}
	files := form.File["audio"]
	switch {
	case len(files) == 0:
		respondError(c, http.StatusBadRequest, "missing_audio", "No file found in the 'audio' field.")
		return
	case len(files) > 1:
		respondError(c, http.StatusBadRequest, "invalid_upload", "Send exactly one file in the 'audio' field.")
		return
	}
	header := files[0]
	if header.Size > maxAudioBytes {
		respondError(c, http.StatusRequestEntityTooLarge, "too_large", "Audio file is too large (limit 10 MiB).")
		return
	}

	// 3. Read the bytes and decide the format from them, not from the name.
	f, err := header.Open()
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_upload", "Could not read the uploaded file.")
		return
	}
	defer f.Close()
	audio, err := io.ReadAll(f)
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid_upload", "Could not read the uploaded file.")
		return
	}
	format, ok := detectAudioFormat(audio)
	if !ok {
		respondError(c, http.StatusUnsupportedMediaType, "unsupported_audio", "Unsupported, truncated or empty audio. Send a WAV or MP3 that contains audio.")
		return
	}

	// 4. Audio -> Note. c.Request.Context() is cancelled if the client hangs
	// up, and the extractor adds its own timeout on top.
	ctx := c.Request.Context()
	note, err := s.Extractor.Extract(ctx, audio, format)
	if err != nil {
		s.upstreamFailed(c, "openai", err)
		return
	}

	// 5. Note -> Notion page. We only get here with a validated Note, and we
	// only answer 201 after Notion has confirmed the write.
	pageURL, err := s.Exporter.CreatePage(ctx, note, time.Now())
	if err != nil {
		s.upstreamFailed(c, "notion", err)
		return
	}

	c.JSON(http.StatusCreated, struct {
		Note
		NotionPageURL string `json:"notion_page_url"`
	}{note, pageURL})
}

var serviceNames = map[string]string{"openai": "OpenAI", "notion": "Notion"}

// upstreamFailed turns an error from step 4 or 5 into a status code and a
// safe message. Only error *types* and upstream codes are logged.
func (s *Server) upstreamFailed(c *gin.Context, stage string, err error) {
	name := serviceNames[stage]
	var up *UpstreamError

	switch {
	case errors.Is(err, context.Canceled):
		// The client hung up; nobody is left to read a response.
		s.Log.Info("client disconnected", "stage", stage)
		c.AbortWithStatus(499)
	case errors.Is(err, context.DeadlineExceeded):
		s.Log.Error("upstream timed out", "stage", stage)
		respondError(c, http.StatusGatewayTimeout, stage+"_timeout", name+" took too long to respond.")
	case errors.Is(err, ErrInvalidNote):
		s.Log.Error("model output failed validation", "reason", err.Error())
		respondError(c, http.StatusBadGateway, "model_output_invalid", "The model's answer failed validation, so nothing was written to Notion.")
	case errors.As(err, &up):
		s.Log.Error("upstream rejected request", "stage", stage, "status", up.Status, "code", up.Code)
		respondError(c, http.StatusBadGateway, stage+"_failed", fmt.Sprintf("%s rejected the request (HTTP %d).", name, up.Status))
	default:
		s.Log.Error("upstream call failed", "stage", stage, "error", err.Error())
		respondError(c, http.StatusBadGateway, stage+"_failed", "Could not reach "+name+".")
	}
}

func respondError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		s.Log.Info("request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds())
	}
}

// recoverPanic replaces gin.Recovery, which would print the panic value and a
// request dump; either could contain note content.
func (s *Server) recoverPanic() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		s.Log.Error("handler panicked")
		respondError(c, http.StatusInternalServerError, "internal_error", "Unexpected server error.")
	})
}
