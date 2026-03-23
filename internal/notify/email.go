package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const ResendEmailURL = "https://api.resend.com/emails"

type ResendEmailRequest struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	HTML    string `json:"html"`
}

type ResendEmailResponse struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Error string `json:"error"`
}

// SendEmail sends an email via Resend API (fire and forget).
// It logs errors but doesn't block or return errors — failures are non-critical.
func SendEmail(ctx context.Context, apiKey, to, subject, htmlBody string) {
	// Fire and forget — run in a goroutine
	go func() {
		if err := sendEmailInternal(ctx, apiKey, to, subject, htmlBody); err != nil {
			fmt.Printf("[notify] error sending email: %v\n", err)
		}
	}()
}

func sendEmailInternal(ctx context.Context, apiKey, to, subject, htmlBody string) error {
	if apiKey == "" || to == "" {
		return fmt.Errorf("missing required email parameters (apiKey: %v, to: %v)", apiKey != "", to != "")
	}

	from := os.Getenv("EMAIL_FROM")
	if from == "" {
		from = "onboarding@resend.dev"
	}
	if envTo := os.Getenv("EMAIL_TO"); envTo != "" {
		to = envTo
	}

	req := ResendEmailRequest{
		From:    from,
		To:      to,
		Subject: subject,
		HTML:    htmlBody,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal email request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", ResendEmailURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("resend api error: status %d, body: %s", resp.StatusCode, string(respBody))
	}

	var resendResp ResendEmailResponse
	if err := json.Unmarshal(respBody, &resendResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if resendResp.Error != "" {
		return fmt.Errorf("resend error: %s", resendResp.Error)
	}

	fmt.Printf("[notify] email sent successfully (id: %s)\n", resendResp.ID)
	return nil
}
