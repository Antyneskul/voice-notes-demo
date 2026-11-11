package main

import (
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfig_AppliesDefaults(t *testing.T) {
	cfg, err := loadConfig(envFrom(map[string]string{
		"OPENAI_API_KEY":        "sk-123",
		"NOTION_API_KEY":        "ntn-456",
		"NOTION_PARENT_PAGE_ID": "0123456789abcdef0123456789abcdef",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OpenAIKey != "sk-123" || cfg.NotionKey != "ntn-456" || cfg.NotionParentID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("credentials not loaded: %+v", cfg)
	}
	if cfg.Port != "8080" || cfg.OpenAIModel != "gpt-audio-1.5" ||
		cfg.OpenAIBaseURL != "https://api.openai.com" || cfg.NotionBaseURL != "https://api.notion.com" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadConfig_Overrides(t *testing.T) {
	cfg, err := loadConfig(envFrom(map[string]string{
		"OPENAI_API_KEY":        "k1",
		"NOTION_API_KEY":        "k2",
		"NOTION_PARENT_PAGE_ID": "0123456789abcdef0123456789abcdef",
		"PORT":                  "9000",
		"OPENAI_MODEL":          "gpt-audio-mini",
		"OPENAI_BASE_URL":       "http://localhost:1111/",
		"NOTION_BASE_URL":       "http://localhost:2222",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "9000" || cfg.OpenAIModel != "gpt-audio-mini" ||
		cfg.OpenAIBaseURL != "http://localhost:1111" || cfg.NotionBaseURL != "http://localhost:2222" {
		t.Errorf("overrides not applied (trailing slash should be trimmed): %+v", cfg)
	}
}

func TestLoadConfig_ListsEveryMissingVariableWithoutValues(t *testing.T) {
	_, err := loadConfig(envFrom(map[string]string{
		"OPENAI_API_KEY": "sk-super-secret",
		"NOTION_API_KEY": "   ", // whitespace-only counts as missing
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	for _, name := range []string{"NOTION_API_KEY", "NOTION_PARENT_PAGE_ID"} {
		if !strings.Contains(msg, name) {
			t.Errorf("error does not mention %s: %q", name, msg)
		}
	}
	if strings.Contains(msg, "OPENAI_API_KEY") {
		t.Errorf("error names a variable that is set: %q", msg)
	}
	if strings.Contains(msg, "sk-super-secret") {
		t.Errorf("error leaks a credential: %q", msg)
	}
}

func envWithParent(id string) map[string]string {
	return map[string]string{"OPENAI_API_KEY": "k1", "NOTION_API_KEY": "k2", "NOTION_PARENT_PAGE_ID": id}
}

func TestLoadConfig_AcceptsParentPageIDWithOrWithoutDashes(t *testing.T) {
	for _, id := range []string{
		"0123456789abcdef0123456789abcdef",
		"01234567-89ab-cdef-0123-456789abcdef",
		"0123456789ABCDEF0123456789ABCDEF",
	} {
		if _, err := loadConfig(envFrom(envWithParent(id))); err != nil {
			t.Errorf("%q should be accepted: %v", id, err)
		}
	}
}

// A mistyped parent page would otherwise only surface after the OpenAI call has run and the
// note has been lost, so the most likely setup mistakes are caught at startup.
func TestLoadConfig_RejectsParentPageIDThatIsNotAPageID(t *testing.T) {
	for name, id := range map[string]string{
		"pasted page URL":  "https://www.notion.so/My-Page-0123456789abcdef0123456789abcdef",
		"too short":        "0123456789abcdef",
		"too long":         "0123456789abcdef0123456789abcdef0",
		"not hex":          "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"id with a suffix": "0123456789abcdef0123456789abcdef?v=1",
	} {
		_, err := loadConfig(envFrom(envWithParent(id)))
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if !strings.Contains(err.Error(), "NOTION_PARENT_PAGE_ID") {
			t.Errorf("%s: error should name the variable: %q", name, err)
		}
		if strings.Contains(err.Error(), id) {
			t.Errorf("%s: error must not echo the value: %q", name, err)
		}
	}
}
