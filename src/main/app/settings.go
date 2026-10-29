package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/cgouz/atlas/src/main/common/crawler"
	llmmodel "github.com/cgouz/atlas/src/main/modules/llm/model"
	llmservice "github.com/cgouz/atlas/src/main/modules/llm/service"
)

const (
	// systemPromptFile is the LLM system prompt; {{unknown_category_code}} in
	// it is replaced with the code from taxonomy.json.
	systemPromptFile = "prompts/normalize_system.txt"
	// outputSchemaFile is the JSON schema of the LLM reply; the allowed codes
	// are added per crawl run from the taxonomy.
	outputSchemaFile = "prompts/normalize_output.schema.json"
)

type Settings struct {
	Crawler      crawler.Settings
	Taxonomy     llmmodel.TaxonomySettings
	SystemPrompt string
	OutputSchema llmservice.OutputSchemaTemplate
}

// LoadSettings reads settings/crawler.json, settings/taxonomy.json, the LLM
// system prompt and the LLM output schema.
func LoadSettings(resources fs.FS) (Settings, error) {
	var s Settings
	if err := readJSON(resources, "settings/crawler.json", &s.Crawler); err != nil {
		return s, err
	}
	if err := readJSON(resources, "settings/taxonomy.json", &s.Taxonomy); err != nil {
		return s, err
	}
	prompt, err := fs.ReadFile(resources, systemPromptFile)
	if err != nil {
		return s, err
	}
	s.SystemPrompt = strings.TrimSpace(strings.ReplaceAll(string(prompt),
		"{{unknown_category_code}}", s.Taxonomy.UnknownCategoryCode))
	schema, err := fs.ReadFile(resources, outputSchemaFile)
	if err != nil {
		return s, err
	}
	if s.OutputSchema, err = llmservice.ParseOutputSchema(schema); err != nil {
		return s, fmt.Errorf("%s: %w", outputSchemaFile, err)
	}
	return s, s.validate()
}

func (s Settings) validate() error {
	var errs []error
	check := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, errors.New(msg))
		}
	}
	c := s.Crawler
	check(c.MaxPagesPerRun > 0, "crawler.json: max_pages_per_run must be > 0")
	check(c.MaxDepth >= 0, "crawler.json: max_depth must be >= 0")
	check(c.MaxSitemaps >= 0, "crawler.json: max_sitemaps must be >= 0")
	check(c.MaxRedirects >= 0, "crawler.json: max_redirects must be >= 0")
	check(c.MaxRetries >= 0, "crawler.json: max_retries must be >= 0")
	check(c.RequestsPerSecond > 0, "crawler.json: requests_per_second must be > 0")
	check(len(c.AllowedContentTypes) > 0, "crawler.json: allowed_content_types is empty")
	check(s.Taxonomy.UnknownCategoryCode != "", "taxonomy.json: unknown_category_code is required")
	check(len(s.Taxonomy.RecordTypes) > 0, "taxonomy.json: record_types is empty")
	check(s.SystemPrompt != "", systemPromptFile+": is empty")
	check(!strings.Contains(s.SystemPrompt, "{{"), systemPromptFile+": unknown {{placeholder}} (only {{unknown_category_code}} is supported)")
	return errors.Join(errs...)
}

func readJSON(fsys fs.FS, name string, v any) error {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
