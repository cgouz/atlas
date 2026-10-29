package service

import (
	"context"

	"github.com/cgouz/atlas/src/main/modules/llm/model"
)

// VLLMClient talks to a vLLM OpenAI-compatible server. Output is constrained
// with response_format json_schema (vLLM structured outputs).
type VLLMClient struct {
	chat chatClient
}

func NewVLLMClient(cfg ClientConfig) *VLLMClient {
	return &VLLMClient{chat: newChatClient(cfg)}
}

func (c *VLLMClient) Runtime() string { return RuntimeVLLM }
func (c *VLLMClient) Model() string   { return c.chat.cfg.Model }

func (c *VLLMClient) Normalize(ctx context.Context, in model.NormalizeInput, schema map[string]any) (model.NormalizeOutput, error) {
	return c.chat.normalize(ctx, in, map[string]any{
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "normalized_record",
				"strict": true,
				"schema": schema,
			},
		},
	})
}
