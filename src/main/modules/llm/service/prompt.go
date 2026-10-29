package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/cgouz/atlas/src/main/modules/llm/model"
)

// The system prompt and the output schema are not here: they live in
// resources/prompts/ (normalize_system.txt, normalize_output.schema.json) and
// are loaded by app.LoadSettings, so both can be tuned without a rebuild.

// enumFields are the schema properties whose allowed values come from the
// taxonomy at run time; the schema file must not list them itself.
var enumFields = []string{"record_type", "category_code", "subcategory_code"}

// OutputSchemaTemplate is the parsed normalize_output.schema.json. It is
// checked once at startup and copied for every request.
type OutputSchemaTemplate struct {
	raw []byte
}

// ParseOutputSchema checks that the schema file describes exactly the fields
// of model.NormalizeOutput, so a rename on one side is caught at startup
// instead of turning every LLM reply into a parse error.
func ParseOutputSchema(data []byte) (OutputSchemaTemplate, error) {
	var schema struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return OutputSchemaTemplate{}, err
	}
	if schema.Type != "object" {
		return OutputSchemaTemplate{}, fmt.Errorf(`type must be "object"`)
	}
	want := outputFields()
	var problems []string
	for _, f := range want {
		if _, ok := schema.Properties[f]; !ok {
			problems = append(problems, fmt.Sprintf("property %q is missing", f))
		}
		if !slices.Contains(schema.Required, f) {
			problems = append(problems, fmt.Sprintf("%q is not in required", f))
		}
	}
	for f := range schema.Properties {
		if !slices.Contains(want, f) {
			problems = append(problems, fmt.Sprintf("property %q is not a field of the LLM output", f))
		}
	}
	for _, f := range enumFields {
		if _, ok := schema.Properties[f]["enum"]; ok {
			problems = append(problems, fmt.Sprintf("property %q must not have an enum: it is filled in from the taxonomy", f))
		}
	}
	if len(problems) > 0 {
		return OutputSchemaTemplate{}, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return OutputSchemaTemplate{raw: data}, nil
}

// outputFields lists the JSON names of model.NormalizeOutput.
func outputFields() []string {
	var fields []string
	for f := range reflect.TypeFor[model.NormalizeOutput]().Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		fields = append(fields, name)
	}
	return fields
}

// OutputSchema is the template with enums for every code, so a
// grammar-constrained runtime cannot produce unknown values.
func (s OutputSchemaTemplate) OutputSchema(t *model.Taxonomy) map[string]any {
	var schema map[string]any
	json.Unmarshal(s.raw, &schema) // checked by ParseOutputSchema
	props := schema["properties"].(map[string]any)

	props["record_type"].(map[string]any)["enum"] = t.RecordTypes
	props["category_code"].(map[string]any)["enum"] = t.CategoryCodes()
	if codes := t.SubcategoryCodes(); len(codes) > 0 {
		enum := make([]any, 0, len(codes)+1)
		for _, c := range codes {
			enum = append(enum, c)
		}
		props["subcategory_code"].(map[string]any)["enum"] = append(enum, nil)
	}
	return schema
}
