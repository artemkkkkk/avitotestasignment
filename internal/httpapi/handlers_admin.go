package httpapi

import (
	"encoding/json"
	"net/http"

	"avitotask/internal/apperr"
	"avitotask/internal/store"

	"github.com/google/uuid"
)

type adminCreateRestaurantRequest struct {
	ExternalID       string `json:"external_id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Category         string `json:"category"`
	Address          string `json:"address"`
	Phone            string `json:"phone"`
	DeliveryFeeCents int    `json:"delivery_fee_cents"`
	MinOrderCents    int    `json:"min_order_cents"`
	ConfirmationURL  string `json:"confirmation_url"`
	WebhookURL       string `json:"webhook_url"`
	WebhookSecret    string `json:"webhook_secret"`
	IsOpen           *bool  `json:"is_open"`
}

func (s *Server) AdminCreateRestaurant(w http.ResponseWriter, r *http.Request) {
	var req adminCreateRestaurantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.BadRequest("invalid request body"))
		return
	}

	if req.Name == "" {
		writeError(w, apperr.BadRequest("name is required"))
		return
	}

	isOpen := false
	if req.IsOpen != nil {
		isOpen = *req.IsOpen
	}

	restaurant, err := s.Store.CreateRestaurant(r.Context(), store.CreateRestaurantParams{
		ExternalID:       req.ExternalID,
		Name:             req.Name,
		Description:      req.Description,
		Category:         req.Category,
		Address:          req.Address,
		Phone:            req.Phone,
		DeliveryFeeCents: req.DeliveryFeeCents,
		MinOrderCents:    req.MinOrderCents,
		ConfirmationURL:  req.ConfirmationURL,
		WebhookURL:       req.WebhookURL,
		WebhookSecret:    req.WebhookSecret,
		IsActive:         true,
		IsOpen:           isOpen,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	keyID, plaintext, err := s.Store.CreateAPIKey(r.Context(), restaurant.ID, "initial")
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"restaurant": toRestaurantInternal(restaurant),
		"api_key_id": keyID,
		"api_key":    plaintext,
	})
}

func (s *Server) AdminListRestaurants(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.AdminListRestaurants(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	resp := make([]restaurantInternalResp, 0, len(items))
	for _, item := range items {
		resp = append(resp, toRestaurantInternal(item))
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
}

type adminUpdateRestaurantRequest struct {
	IsActive *bool `json:"is_active"`
	IsOpen   *bool `json:"is_open"`
}

func (s *Server) AdminUpdateRestaurant(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := uuid.Parse(r.PathValue("restaurantId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid restaurant id"))
		return
	}

	var req adminUpdateRestaurantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.BadRequest("invalid request body"))
		return
	}

	updated, err := s.Store.AdminUpdateRestaurant(r.Context(), restaurantID, store.AdminUpdateRestaurantParams{
		IsActive: req.IsActive,
		IsOpen:   req.IsOpen,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toRestaurantInternal(updated))
}

type adminCreateAPIKeyRequest struct {
	Name string `json:"name"`
}

func (s *Server) AdminCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := uuid.Parse(r.PathValue("restaurantId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid restaurant id"))
		return
	}

	var req adminCreateAPIKeyRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	keyID, plaintext, err := s.Store.CreateAPIKey(r.Context(), restaurantID, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      keyID,
		"api_key": plaintext,
	})
}

func (s *Server) AdminRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	keyID, err := uuid.Parse(r.PathValue("keyId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid key id"))
		return
	}

	if err := s.Store.RevokeAPIKey(r.Context(), keyID); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{Status: "revoked"})
}
