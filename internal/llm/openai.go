// Package llm provides a minimal OpenAI GPT-4o client for structured incentive extraction.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const openAIURL = "https://api.openai.com/v1/chat/completions"

// maxContentChars is the character limit sent to the LLM.
// HTML pages can be hundreds of KB; this keeps costs and latency reasonable.
const maxContentChars = 7000

// LLMExtraction holds all fields GPT-4o attempts to extract from raw incentive content.
type LLMExtraction struct {
	ProgramName          string   `json:"program_name"`
	UtilityCompany       string   `json:"utility_company"`
	IncentiveDescription string   `json:"incentive_description"`
	IncentiveAmount      *float64 `json:"incentive_amount"`
	MaximumAmount        *float64 `json:"maximum_amount"`
	PercentValue         *float64 `json:"percent_value"`
	PerUnitAmount        *float64 `json:"per_unit_amount"`
	UnitType             string   `json:"unit_type"`
	IncentiveFormat      string   `json:"incentive_format"`
	State                string   `json:"state"`
	Categories           []string `json:"categories"`
	CustomerType         string   `json:"customer_type"`
	StartDate            string   `json:"start_date"`
	EndDate              string   `json:"end_date"`
	ApplicationURL       string   `json:"application_url"`
	ProgramURL           string   `json:"program_url"`
	ContactEmail         string   `json:"contact_email"`
	ContactPhone         string   `json:"contact_phone"`
	ContractorRequired   *bool    `json:"contractor_required"`
	EnergyAuditRequired  *bool    `json:"energy_audit_required"`
	WhileFundsLast       bool     `json:"while_funds_last"`
	IncomeQualified      bool     `json:"income_qualified"`
	EligibilityNotes     string   `json:"eligibility_notes"`
}

var (
	reHTMLTag  = regexp.MustCompile(`<[^>]+>`)
	reSpaces   = regexp.MustCompile(`[ \t]{2,}`)
	reNewlines = regexp.MustCompile(`\n{3,}`)
)

// prepareContent strips HTML tags (if applicable) and truncates to maxContentChars.
func prepareContent(raw, contentType string) string {
	s := raw
	if strings.Contains(contentType, "html") || strings.HasPrefix(strings.TrimSpace(raw), "<") {
		s = reHTMLTag.ReplaceAllString(raw, " ")
		s = reSpaces.ReplaceAllString(s, " ")
		s = reNewlines.ReplaceAllString(s, "\n\n")
		s = strings.TrimSpace(s)
	}
	if len(s) > maxContentChars {
		s = s[:maxContentChars] + "\n[content truncated]"
	}
	return s
}

const systemPrompt = `You are an expert at extracting structured data from energy rebate and incentive program content. Extract all available information accurately. Return ONLY valid JSON — no markdown, no explanation.`

const userPromptTpl = `Extract all available incentive program data from the content below. Return a JSON object with these exact fields (use null for fields not present in the content):

{
  "program_name": "full program name",
  "utility_company": "the utility, government body, or organization offering this",
  "incentive_description": "complete description, up to 800 chars",
  "incentive_amount": number or null (base dollar amount, e.g. 500),
  "maximum_amount": number or null (maximum/cap dollar amount, e.g. 1500),
  "percent_value": number or null (rebate as a percent 0–100, e.g. 25),
  "per_unit_amount": number or null (e.g. 30 for $30/ton),
  "unit_type": "string or null — e.g. kilowatt, ton, lamp, watt",
  "incentive_format": "one of: dollar_amount, percent, per_unit, tiered, narrative",
  "state": "2-letter US state code or null",
  "categories": ["from: HVAC, Solar, Water Heating, Electric Vehicles, Weatherization, Battery Storage, Lighting, Appliances, Demand Response, Other"],
  "customer_type": "Residential or Commercial or Residential, Commercial or null",
  "start_date": "YYYY-MM-DD or null",
  "end_date": "YYYY-MM-DD or null",
  "application_url": "direct application/enrollment URL or null",
  "program_url": "main program information URL or null",
  "contact_email": "string or null",
  "contact_phone": "string or null",
  "contractor_required": true/false/null,
  "energy_audit_required": true/false/null,
  "while_funds_last": true or false,
  "income_qualified": true or false,
  "eligibility_notes": "income thresholds, AMI limits, equipment specs, or other eligibility criteria — null if none"
}

Content:
%s`

// Client is a minimal OpenAI REST client for incentive extraction.
type Client struct {
	apiKey     string
	httpClient *http.Client
	// Debug, when true, prints the prepared content and raw LLM response to
	// stderr so callers can see exactly what was sent and received.
	Debug bool
}

// NewClient returns a Client using the given OpenAI API key.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 90 * time.Second},
	}
}

// WithDebug enables or disables debug output and returns the client for chaining.
func (c *Client) WithDebug(debug bool) *Client {
	c.Debug = debug
	return c
}

// ExtractIncentive sends raw content to GPT-4o and returns a structured extraction.
func (c *Client) ExtractIncentive(rawContent, contentType string) (*LLMExtraction, error) {
	content := prepareContent(rawContent, contentType)

	if c.Debug {
		preview := content
		if len(preview) > 800 {
			preview = preview[:800] + fmt.Sprintf("\n... [%d more chars]", len(content)-800)
		}
		fmt.Fprintf(os.Stderr, "\n[LLM DEBUG] prepared content (%s, %d chars):\n─────────────────────────────────────────\n%s\n─────────────────────────────────────────\n\n",
			contentType, len(content), preview)
	}

	body, err := json.Marshal(map[string]any{
		"model":           "gpt-4o",
		"response_format": map[string]string{"type": "json_object"},
		"temperature":     0,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": fmt.Sprintf(userPromptTpl, content)},
		},
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, openAIURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if c.Debug {
		fmt.Fprintf(os.Stderr, "[LLM DEBUG] raw API response:\n─────────────────────────────────────────\n%s\n─────────────────────────────────────────\n\n",
			string(respBody))
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse api response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("openai returned no choices")
	}

	var ext LLMExtraction
	if err := json.Unmarshal([]byte(apiResp.Choices[0].Message.Content), &ext); err != nil {
		return nil, fmt.Errorf("parse extraction JSON: %w", err)
	}

	if c.Debug {
		pretty, _ := json.MarshalIndent(ext, "", "  ")
		fmt.Fprintf(os.Stderr, "[LLM DEBUG] parsed extraction:\n─────────────────────────────────────────\n%s\n─────────────────────────────────────────\n\n",
			string(pretty))
	}

	return &ext, nil
}
