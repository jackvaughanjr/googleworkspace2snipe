package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client sends messages to a Slack incoming webhook URL.
// All methods are no-ops if WebhookURL is empty.
type Client struct {
	webhookURL string
	httpClient *http.Client
}

// NewClient returns a new Client. If webhookURL is empty, all Send calls
// are silently ignored.
func NewClient(webhookURL string) *Client {
	return &Client{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send posts a plain-text message to the configured webhook. Returns nil
// immediately if no webhook URL is set.
func (c *Client) Send(ctx context.Context, text string) error {
	if c.webhookURL == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook: unexpected status %d", resp.StatusCode)
	}
	return nil
}
