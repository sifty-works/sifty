package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type openaiAdapter struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
}

func newOpenAI(endpoint, model, keyEnv string, timeoutMS, _ int, secrets map[string]string) (Model, error) {
	t := time.Duration(timeoutMS) * time.Millisecond
	if t <= 0 {
		t = 2 * time.Minute
	}
	return &openaiAdapter{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    model,
		apiKey:   resolveKey(keyEnv, secrets),
		client:   &http.Client{Timeout: t},
	}, nil
}

func resolveKey(keyEnv string, secrets map[string]string) string {
	if v := os.Getenv(keyEnv); v != "" {
		return v
	}
	return secrets[keyEnv]
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiReq struct {
	Model    string          `json:"model"`
	Messages []openaiMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openaiResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (a *openaiAdapter) Complete(ctx context.Context, r Request) (Response, error) {
	body := openaiReq{
		Model:    a.model,
		Messages: []openaiMessage{{Role: "user", Content: r.Prompt}},
		Stream:   false,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("openai: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return Response{}, fmt.Errorf("openai: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.apiKey)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("openai: status %d", resp.StatusCode)
	}
	var out openaiResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("openai: decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("openai: no choices in response")
	}
	return Response{
		Text:      out.Choices[0].Message.Content,
		InTokens:  out.Usage.PromptTokens,
		OutTokens: out.Usage.CompletionTokens,
	}, nil
}
