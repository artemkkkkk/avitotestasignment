package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"avitotask/internal/hmacsign"

	"github.com/google/uuid"
)

type ConfirmItem struct {
	MenuItemID uuid.UUID
	Quantity   int
}

type ConfirmInput struct {
	OrderID         uuid.UUID
	RestaurantID    uuid.UUID
	ConfirmationURL string
	TimeoutMS       int
	WebhookSecret   string
	ExpiresAt       time.Time
	PaymentMethod   string
	Items           []ConfirmItem
}

type ConfirmResult struct {
	Confirmed        bool
	ExternalOrderRef string
	Comment          string
}

type OrderConfirmer interface {
	Confirm(ctx context.Context, in ConfirmInput) (ConfirmResult, error)
}

type confirmationRequest struct {
	OrderID       uuid.UUID          `json:"order_id"`
	RestaurantID  uuid.UUID          `json:"restaurant_id"`
	ExpiresAt     time.Time          `json:"expires_at"`
	PaymentMethod string             `json:"payment_method"`
	Items         []confirmationItem `json:"items"`
}

type confirmationItem struct {
	MenuItemID uuid.UUID `json:"menu_item_id"`
	Quantity   int       `json:"quantity"`
}

type confirmationResponse struct {
	Confirmed        bool   `json:"confirmed"`
	ExternalOrderRef string `json:"external_order_ref"`
	Comment          string `json:"comment"`
}

type HTTPConfirmer struct {
	client *http.Client
}

func NewHTTPConfirmer() *HTTPConfirmer {
	return &HTTPConfirmer{client: &http.Client{}}
}

func (c *HTTPConfirmer) Confirm(ctx context.Context, in ConfirmInput) (ConfirmResult, error) {
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	items := make([]confirmationItem, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, confirmationItem{MenuItemID: item.MenuItemID, Quantity: item.Quantity})
	}

	body, err := json.Marshal(confirmationRequest{
		OrderID:       in.OrderID,
		RestaurantID:  in.RestaurantID,
		ExpiresAt:     in.ExpiresAt,
		PaymentMethod: in.PaymentMethod,
		Items:         items,
	})
	if err != nil {
		return ConfirmResult{}, fmt.Errorf("marshal confirmation payload: %w", err)
	}

	req, err := http.NewRequestWithContext(cctx, http.MethodPost, in.ConfirmationURL, bytes.NewReader(body))
	if err != nil {
		return ConfirmResult{}, fmt.Errorf("create confirmation request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Avito-Kitchen-Request-Id", in.OrderID.String())
	if in.WebhookSecret != "" {
		req.Header.Set("X-Avito-Kitchen-Signature", hmacsign.Sign(in.WebhookSecret, body))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return ConfirmResult{}, fmt.Errorf("confirmation request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ConfirmResult{Confirmed: false, Comment: string(respBody)}, nil
	}

	var dto confirmationResponse
	if err := json.Unmarshal(respBody, &dto); err != nil {
		return ConfirmResult{Confirmed: false, Comment: "invalid restaurant response"}, nil
	}

	return ConfirmResult{
		Confirmed:        dto.Confirmed,
		ExternalOrderRef: dto.ExternalOrderRef,
		Comment:          dto.Comment,
	}, nil
}
