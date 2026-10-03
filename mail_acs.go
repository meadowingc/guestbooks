package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

var mailHTTPClient = &http.Client{Timeout: mailSendTimeout}

const maxACSResponseBytes = 64 << 10

func sendACS(ctx context.Context, client *http.Client, config mailerConfig, recipient, subject, body string, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, mailSendTimeout)
	defer cancel()
	payload := struct {
		SenderAddress string `json:"senderAddress"`
		Content       struct {
			Subject   string `json:"subject"`
			PlainText string `json:"plainText"`
		} `json:"content"`
		Recipients struct {
			To []struct {
				Address string `json:"address"`
			} `json:"to"`
		} `json:"recipients"`
	}{SenderAddress: mailEnvelopeAddress(config.from)}
	payload.Content.Subject, payload.Content.PlainText = subject, body
	payload.Recipients.To = append(payload.Recipients.To, struct {
		Address string `json:"address"`
	}{recipient})
	encoded, err := json.Marshal(payload)
	if err != nil {
		return mailFailure(ctx, "azure_communication_service", "prepare", "invalid_input", 0, err)
	}
	endpoint := *config.endpoint
	endpoint.Path = "/emails:send"
	endpoint.RawQuery = "api-version=2023-03-31"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return mailFailure(ctx, "azure_communication_service", "prepare", "invalid_input", 0, err)
	}
	hash := sha256.Sum256(encoded)
	contentHash := base64.StdEncoding.EncodeToString(hash[:])
	date := now.UTC().Format(http.TimeFormat)
	toSign := "POST\n" + endpoint.RequestURI() + "\n" + date + ";" + endpoint.Host + ";" + contentHash
	mac := hmac.New(sha256.New, config.key)
	_, _ = mac.Write([]byte(toSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-ms-date", date)
	request.Header.Set("x-ms-content-sha256", contentHash)
	request.Header.Set("Authorization", "HMAC-SHA256 SignedHeaders=x-ms-date;host;x-ms-content-sha256&Signature="+signature)

	// Never forward a signed request, including to a redirect on the same host.
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := boundedClient.Do(request)
	if err != nil {
		return mailFailure(ctx, "azure_communication_service", "request", "transport", 0, err)
	}
	defer response.Body.Close()
	count, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxACSResponseBytes+1))
	if err != nil {
		return mailFailure(ctx, "azure_communication_service", "response", "transport", response.StatusCode, err)
	}
	if count > maxACSResponseBytes {
		return mailFailure(ctx, "azure_communication_service", "response", "invalid_response", response.StatusCode, nil)
	}
	if response.StatusCode != http.StatusAccepted {
		category := "invalid_response"
		switch {
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			category = "authentication"
		case response.StatusCode == http.StatusTooManyRequests:
			category = "rate_limited"
		case response.StatusCode >= 500:
			category = "provider_failure"
		case response.StatusCode >= 400:
			category = "permanent_rejection"
		}
		return mailFailure(ctx, "azure_communication_service", "response", category, response.StatusCode, nil)
	}
	return nil
}
