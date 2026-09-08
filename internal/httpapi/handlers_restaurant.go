package httpapi

import (
	"encoding/json"
	"net/http"

	"avitotask/internal/apperr"
	"avitotask/internal/service"
	"avitotask/internal/store"

	"github.com/google/uuid"
)

func (s *Server) RestaurantMe(w http.ResponseWriter, r *http.Request) {
	restaurant, ok := restaurantFromContext(r.Context())
	if !ok {
		writeError(w, apperr.Unauthorized("unauthorized"))
		return
	}

	writeJSON(w, http.StatusOK, toRestaurantInternal(restaurant))
}

type updateRestaurantRequest struct {
	Name             *string `json:"name"`
	Description      *string `json:"description"`
	Address          *string `json:"address"`
	Phone            *string `json:"phone"`
	DeliveryFeeCents *int    `json:"delivery_fee_cents"`
	MinOrderCents    *int    `json:"min_order_cents"`
	IsOpen           *bool   `json:"is_open"`
	ConfirmationURL  *string `json:"confirmation_url"`
	WebhookURL       *string `json:"webhook_url"`
}

func (s *Server) UpdateRestaurantMe(w http.ResponseWriter, r *http.Request) {
	restaurant, ok := restaurantFromContext(r.Context())
	if !ok {
		writeError(w, apperr.Unauthorized("unauthorized"))
		return
	}

	var req updateRestaurantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.BadRequest("invalid request body"))
		return
	}

	updated, err := s.Orders.UpdateRestaurantMe(r.Context(), restaurant.ID, store.UpdateRestaurantParams{
		Name:             req.Name,
		Description:      req.Description,
		Address:          req.Address,
		Phone:            req.Phone,
		DeliveryFeeCents: req.DeliveryFeeCents,
		MinOrderCents:    req.MinOrderCents,
		IsOpen:           req.IsOpen,
		ConfirmationURL:  req.ConfirmationURL,
		WebhookURL:       req.WebhookURL,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toRestaurantInternal(updated))
}

func (s *Server) SyncMenu(w http.ResponseWriter, r *http.Request) {
	restaurant, ok := restaurantFromContext(r.Context())
	if !ok {
		writeError(w, apperr.Unauthorized("unauthorized"))
		return
	}

	var req store.SyncMenuRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.BadRequest("invalid request body"))
		return
	}

	if err := s.Orders.SyncMenu(r.Context(), restaurant.ID, req); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

func (s *Server) ListRestaurantOrders(w http.ResponseWriter, r *http.Request) {
	restaurant, ok := restaurantFromContext(r.Context())
	if !ok {
		writeError(w, apperr.Unauthorized("unauthorized"))
		return
	}

	status := r.URL.Query().Get("status")

	orders, err := s.Orders.ListRestaurantOrders(r.Context(), restaurant.ID, status)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := make([]orderResp, 0, len(orders))
	for _, order := range orders {
		resp = append(resp, toOrderResp(order))
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": resp})
}

type cancelRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) RestaurantStartPreparing(w http.ResponseWriter, r *http.Request) {
	s.restaurantAction(w, r, service.ActionPreparing, "")
}

func (s *Server) RestaurantStartDelivering(w http.ResponseWriter, r *http.Request) {
	s.restaurantAction(w, r, service.ActionDelivering, "")
}

func (s *Server) RestaurantComplete(w http.ResponseWriter, r *http.Request) {
	s.restaurantAction(w, r, service.ActionComplete, "")
}

func (s *Server) RestaurantCancel(w http.ResponseWriter, r *http.Request) {
	var req cancelRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.restaurantAction(w, r, service.ActionCancel, req.Reason)
}

func (s *Server) restaurantAction(w http.ResponseWriter, r *http.Request, action service.RestaurantAction, reason string) {
	restaurant, ok := restaurantFromContext(r.Context())
	if !ok {
		writeError(w, apperr.Unauthorized("unauthorized"))
		return
	}

	orderID, err := uuid.Parse(r.PathValue("orderId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid order id"))
		return
	}

	if err := s.Orders.RestaurantAction(r.Context(), restaurant.ID, orderID, action, reason); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{Status: string(action)})
}
