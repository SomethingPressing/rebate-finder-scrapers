package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	openAIEmbeddingURL = "https://api.openai.com/v1/embeddings"
	embeddingModel     = "text-embedding-3-small"
	classifyModel      = "gpt-4o-mini"
)

// Embed returns the embedding vector for text using text-embedding-3-small.
func (c *Client) Embed(text string) ([]float32, error) {
	body, err := json.Marshal(map[string]string{
		"input": text,
		"model": embeddingModel,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, openAIEmbeddingURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embed read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed HTTP %d: %s", resp.StatusCode, raw)
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("embed decode: %w", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty response")
	}
	return result.Data[0].Embedding, nil
}

// ClassifyCategory asks GPT-4o mini to pick the single best taxonomy category
// for the given product description. Returns "" when no category fits.
func (c *Client) ClassifyCategory(productDescription string, categories []string) (string, error) {
	prompt := "You are an energy rebate program classifier.\n" +
		"Given a rebate product name, return ONLY the single most appropriate category from the list below.\n" +
		"If none apply, return exactly: Other\n\n" +
		"Product: " + productDescription + "\n\n" +
		"Categories:\n" + strings.Join(categories, "\n")

	body, err := json.Marshal(map[string]any{
		"model":       classifyModel,
		"temperature": 0,
		"max_tokens":  20,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, openAIURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("classify request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("classify read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("classify HTTP %d: %s", resp.StatusCode, raw)
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &apiResp); err != nil || len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("classify parse: %w", err)
	}

	cat := strings.TrimSpace(apiResp.Choices[0].Message.Content)
	if cat == "Other" || cat == "" {
		return "", nil
	}
	return cat, nil
}
