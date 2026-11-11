package main

import "testing"

func TestErrorCode(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"openai code", `{"error":{"code":"rate_limit_exceeded","type":"requests"}}`, "rate_limit_exceeded"},
		{"openai type when code is null", `{"error":{"code":null,"type":"invalid_request_error"}}`, "invalid_request_error"},
		{"notion code", `{"object":"error","code":"object_not_found","message":"Could not find page"}`, "object_not_found"},
		{"short lowercase token that is not a known code", `{"error":{"code":"acme_dana_4111"}}`, "unrecognized"},
		{"a single lowercase word, like a customer name", `{"error":{"code":"dana"}}`, "unrecognized"},
		{"looks machine-like but is unknown", `{"error":{"code":"model.not-found"}}`, "unrecognized"},
		{"no code at all", `{"error":{"message":"something"}}`, ""},
		{"not json", `<html>502 Bad Gateway</html>`, ""},
		{"sentence", `{"error":{"code":"Dana wants a discount on the Acme fleet plan"}}`, "unrecognized"},
		{"upper case", `{"error":{"code":"Rate_Limit"}}`, "unrecognized"},
		{"digits and punctuation from a card number", `{"error":{"code":"4111-1111-1111-1111 exp 12/29"}}`, "unrecognized"},
		{"too long", `{"error":{"code":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`, "unrecognized"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorCode([]byte(tc.body)); got != tc.want {
				t.Fatalf("errorCode = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every code Notion documents (https://developers.notion.com/reference/status-codes)
// must be logged as-is; they are the diagnostic value of keeping a code at all.
func TestErrorCode_KnownNotionCodes(t *testing.T) {
	for _, code := range []string{
		"invalid_json", "invalid_request_url", "invalid_request", "invalid_grant", "validation_error",
		"missing_version", "invalid_beta", "unauthorized", "restricted_resource", "object_not_found",
		"conflict_error", "rate_limited", "internal_server_error", "bad_gateway", "service_unavailable",
		"database_connection_unavailable", "gateway_timeout", "service_overload",
	} {
		if got := errorCode([]byte(`{"object":"error","code":"` + code + `"}`)); got != code {
			t.Errorf("notion code %q was logged as %q", code, got)
		}
	}
}

func TestErrorCode_KnownOpenAICodes(t *testing.T) {
	for _, code := range []string{"invalid_api_key", "rate_limit_exceeded", "insufficient_quota", "model_not_found", "context_length_exceeded"} {
		if got := errorCode([]byte(`{"error":{"code":"` + code + `"}}`)); got != code {
			t.Errorf("openai code %q was logged as %q", code, got)
		}
	}
}
