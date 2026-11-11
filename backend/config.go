package main

import (
	"fmt"
	"regexp"
	"strings"
)

// A Notion page ID is 32 hex characters, with or without dashes.
var pageIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

type Config struct {
	OpenAIKey      string
	OpenAIModel    string
	OpenAIBaseURL  string
	NotionKey      string
	NotionParentID string
	NotionBaseURL  string
	Port           string
}

// loadConfig reads settings through `getenv` (os.Getenv in production) so tests
// don't have to touch the real environment. It reports every missing required
// variable at once, by name only: values never appear in errors.
func loadConfig(getenv func(string) string) (Config, error) {
	get := func(name, fallback string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return fallback
	}

	cfg := Config{
		OpenAIKey:      get("OPENAI_API_KEY", ""),
		NotionKey:      get("NOTION_API_KEY", ""),
		NotionParentID: get("NOTION_PARENT_PAGE_ID", ""),
		OpenAIModel:    get("OPENAI_MODEL", "gpt-audio-1.5"),
		OpenAIBaseURL:  strings.TrimRight(get("OPENAI_BASE_URL", "https://api.openai.com"), "/"),
		NotionBaseURL:  strings.TrimRight(get("NOTION_BASE_URL", "https://api.notion.com"), "/"),
		Port:           get("PORT", "8080"),
	}

	var missing []string
	for _, r := range []struct{ name, value string }{
		{"OPENAI_API_KEY", cfg.OpenAIKey},
		{"NOTION_API_KEY", cfg.NotionKey},
		{"NOTION_PARENT_PAGE_ID", cfg.NotionParentID},
	} {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %s (copy .env.example to .env and fill them in)",
			strings.Join(missing, ", "))
	}
	// A mistyped parent page (for example a pasted URL) would only fail after the OpenAI call
	// has run and the note has been lost, so say so now. The value is never echoed.
	if !pageIDPattern.MatchString(strings.ReplaceAll(cfg.NotionParentID, "-", "")) {
		return Config{}, fmt.Errorf("NOTION_PARENT_PAGE_ID must be the page's 32-character ID, with or without dashes (a pasted page URL won't work)")
	}
	return cfg, nil
}
