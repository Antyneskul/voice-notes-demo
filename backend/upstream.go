package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// UpstreamError is a non-2xx answer from OpenAI or Notion. It deliberately
// keeps only the HTTP status and the short machine-readable error code, not
// the response body, so nothing the upstream echoes back can reach our logs
// or our clients.
type UpstreamError struct {
	Service string // "openai" or "notion"
	Status  int
	Code    string
}

func (e *UpstreamError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s returned HTTP %d (%s)", e.Service, e.Status, e.Code)
	}
	return fmt.Sprintf("%s returned HTTP %d", e.Service, e.Status)
}

const maxUpstreamResponse = 1 << 20 // 1 MiB is far more than either API sends back here

// postJSON sends `body` as JSON and decodes a 2xx JSON reply into `out`.
// Cancellation and deadlines come from ctx; transport errors wrap ctx.Err(), so
// callers can use errors.Is(err, context.DeadlineExceeded) / context.Canceled.
func postJSON(ctx context.Context, client *http.Client, service, url string, headers map[string]string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encoding request: %w", service, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%s: building request: %w", service, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", service, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamResponse))
	if err != nil {
		return fmt.Errorf("%s: reading response: %w", service, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &UpstreamError{Service: service, Status: resp.StatusCode, Code: errorCode(raw)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &UpstreamError{Service: service, Status: resp.StatusCode, Code: "invalid_response"}
	}
	return nil
}

// knownCodes are the error codes we are willing to log. The response body is
// controlled by the provider, or by whatever proxy OPENAI_BASE_URL points at,
// and can contain anything, so a code is logged only if it is on this fixed
// list. Anything else is logged as "unrecognized"; the HTTP status is always
// logged either way. Notion's codes are from its documented status codes.
var knownCodes = map[string]bool{
	// OpenAI
	"invalid_api_key": true, "rate_limit_exceeded": true, "insufficient_quota": true,
	"model_not_found": true, "context_length_exceeded": true, "invalid_request_error": true,
	"authentication_error": true, "permission_error": true, "not_found_error": true,
	"rate_limit_error": true, "server_error": true, "requests": true, "tokens": true,
	// Notion
	"invalid_json": true, "invalid_request_url": true, "invalid_request": true, "invalid_grant": true,
	"validation_error": true, "missing_version": true, "invalid_beta": true, "unauthorized": true,
	"restricted_resource": true, "object_not_found": true, "conflict_error": true, "rate_limited": true,
	"internal_server_error": true, "bad_gateway": true, "service_unavailable": true,
	"database_connection_unavailable": true, "gateway_timeout": true, "service_overload": true,
}

// errorCode pulls the short error code out of either API's error shape:
// OpenAI: {"error":{"code":"...","type":"..."}}   Notion: {"code":"..."}.
func errorCode(raw []byte) string {
	var e struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e) // best effort; a partial parse is fine
	code := e.Error.Code
	if code == "" {
		code = e.Error.Type
	}
	if code == "" {
		code = e.Code
	}
	if code != "" && !knownCodes[code] {
		return "unrecognized"
	}
	return code
}
