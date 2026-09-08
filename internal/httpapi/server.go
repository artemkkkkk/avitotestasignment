package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"avitotask/internal/apperr"
	"avitotask/internal/applog"
	"avitotask/internal/domain"
	"avitotask/internal/service"
	"avitotask/internal/store"

	"github.com/google/uuid"
)

type ctxKey string

const restaurantCtxKey ctxKey = "restaurant"

type Server struct {
	Store      *store.Store
	Orders     *service.OrderService
	BaseURL    string
	AdminToken string
}

func NewServer(st *store.Store, orders *service.OrderService, baseURL string) *Server {
	return &Server{Store: st, Orders: orders, BaseURL: baseURL}
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.wrap(s.ServeUI))
	mux.HandleFunc("GET /health", s.wrap(s.Health))

	mux.HandleFunc("GET /api/v1/restaurants", s.wrap(s.ListRestaurants))
	mux.HandleFunc("GET /api/v1/restaurants/{restaurantId}", s.wrap(s.GetRestaurant))
	mux.HandleFunc("GET /api/v1/restaurants/{restaurantId}/menu", s.wrap(s.GetRestaurantMenu))

	mux.HandleFunc("POST /api/v1/orders", s.wrap(s.CreateOrder))
	mux.HandleFunc("GET /api/v1/orders/{orderId}", s.wrap(s.GetOrder))
	mux.HandleFunc("POST /api/v1/orders/{orderId}/cancel", s.wrap(s.CancelOrder))

	mux.HandleFunc("GET /mock/pay/{paymentToken}", s.wrap(s.MockPayPage))
	mux.HandleFunc("POST /mock/pay/{paymentToken}/pay", s.wrap(s.MockPay))

	mux.HandleFunc("GET /api/v1/restaurant/me", s.wrap(s.requireRestaurant(s.RestaurantMe)))
	mux.HandleFunc("PATCH /api/v1/restaurant/me", s.wrap(s.requireRestaurant(s.UpdateRestaurantMe)))
	mux.HandleFunc("POST /api/v1/restaurant/menu/sync", s.wrap(s.requireRestaurant(s.SyncMenu)))
	mux.HandleFunc("GET /api/v1/restaurant/orders", s.wrap(s.requireRestaurant(s.ListRestaurantOrders)))
	mux.HandleFunc("POST /api/v1/restaurant/orders/{orderId}/preparing", s.wrap(s.requireRestaurant(s.RestaurantStartPreparing)))
	mux.HandleFunc("POST /api/v1/restaurant/orders/{orderId}/delivering", s.wrap(s.requireRestaurant(s.RestaurantStartDelivering)))
	mux.HandleFunc("POST /api/v1/restaurant/orders/{orderId}/complete", s.wrap(s.requireRestaurant(s.RestaurantComplete)))
	mux.HandleFunc("POST /api/v1/restaurant/orders/{orderId}/cancel", s.wrap(s.requireRestaurant(s.RestaurantCancel)))

	mux.HandleFunc("POST /api/v1/admin/restaurants", s.wrap(s.requireAdmin(s.AdminCreateRestaurant)))
	mux.HandleFunc("GET /api/v1/admin/restaurants", s.wrap(s.requireAdmin(s.AdminListRestaurants)))
	mux.HandleFunc("PATCH /api/v1/admin/restaurants/{restaurantId}", s.wrap(s.requireAdmin(s.AdminUpdateRestaurant)))
	mux.HandleFunc("POST /api/v1/admin/restaurants/{restaurantId}/api-keys", s.wrap(s.requireAdmin(s.AdminCreateAPIKey)))
	mux.HandleFunc("POST /api/v1/admin/api-keys/{keyId}/revoke", s.wrap(s.requireAdmin(s.AdminRevokeAPIKey)))

	return mux
}

// wrap = логирование запроса (внешний слой) + recover (внутренний слой).
func (s *Server) wrap(next http.HandlerFunc) http.HandlerFunc {
	logged := applog.HTTPMiddleware(s.recoverMW(next))
	return func(w http.ResponseWriter, r *http.Request) {
		logged.ServeHTTP(w, r)
	}
}

func (s *Server) recoverMW(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				applog.Error("panic recovered",
					"request_id", w.Header().Get("X-Request-ID"),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprintf("%v", rec),
				)
				writeError(w, apperr.Internal("internal server error"))
			}
		}()
		next(w, r)
	}
}

func (s *Server) requireRestaurant(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, apperr.Unauthorized("missing bearer token"))
			return
		}

		key := strings.TrimPrefix(auth, "Bearer ")
		keyHash := hashAPIKey(key)

		restaurant, err := s.Store.GetRestaurantByAPIKeyHash(r.Context(), keyHash)
		if err != nil {
			writeError(w, err)
			return
		}

		ctx := context.WithValue(r.Context(), restaurantCtxKey, restaurant)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" {
			writeError(w, apperr.New(http.StatusServiceUnavailable, "ADMIN_DISABLED", "admin api is disabled"))
			return
		}
		if r.Header.Get("X-Admin-Token") != s.AdminToken {
			writeError(w, apperr.Unauthorized("invalid admin token"))
			return
		}
		next(w, r)
	}
}

func restaurantFromContext(ctx context.Context) (domain.Restaurant, bool) {
	r, ok := ctx.Value(restaurantCtxKey).(domain.Restaurant)
	return r, ok
}

func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError логирует ошибку: 4xx как Warn (бизнес-отказ), 5xx и unhandled как Error.
// request_id берётся из заголовка, который проставил middleware логирования.
func writeError(w http.ResponseWriter, err error) {
	reqID := w.Header().Get("X-Request-ID")

	var appErr apperr.Error
	if errors.As(err, &appErr) {
		if appErr.Status >= 500 {
			applog.Error("request failed with server error",
				"request_id", reqID,
				"code", appErr.Code,
				"message", appErr.Message,
				"status", appErr.Status,
				"error", err.Error(),
			)
		} else {
			applog.Warn("request rejected",
				"request_id", reqID,
				"code", appErr.Code,
				"message", appErr.Message,
				"status", appErr.Status,
			)
		}

		writeJSON(w, appErr.Status, map[string]any{
			"error": map[string]any{
				"code":    appErr.Code,
				"message": appErr.Message,
				"details": appErr.Details,
			},
		})
		return
	}

	applog.Error("unhandled error",
		"request_id", reqID,
		"error", err.Error(),
	)
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]any{
			"code":    "INTERNAL_ERROR",
			"message": "internal server error",
		},
	})
}

type restaurantPublicResp struct {
	ID                       uuid.UUID `json:"id"`
	ExternalID               *string   `json:"external_id"`
	Name                     string    `json:"name"`
	Description              *string   `json:"description"`
	Category                 *string   `json:"category"`
	Address                  *string   `json:"address"`
	Phone                    *string   `json:"phone"`
	DeliveryFeeCents         int       `json:"delivery_fee_cents"`
	MinOrderCents            int       `json:"min_order_cents"`
	EstimatedDeliveryMinutes *int      `json:"estimated_delivery_minutes"`
	IsOpen                   bool      `json:"is_open"`
}

type restaurantInternalResp struct {
	restaurantPublicResp
	IsActive        bool    `json:"is_active"`
	ConfirmationURL *string `json:"confirmation_url"`
	WebhookURL      *string `json:"webhook_url"`
}

func toRestaurantPublic(r domain.Restaurant) restaurantPublicResp {
	return restaurantPublicResp{
		ID:                       r.ID,
		ExternalID:               r.ExternalID,
		Name:                     r.Name,
		Description:              r.Description,
		Category:                 r.Category,
		Address:                  r.Address,
		Phone:                    r.Phone,
		DeliveryFeeCents:         r.DeliveryFeeCents,
		MinOrderCents:            r.MinOrderCents,
		EstimatedDeliveryMinutes: r.EstimatedDeliveryMinutes,
		IsOpen:                   r.IsOpen,
	}
}

func toRestaurantInternal(r domain.Restaurant) restaurantInternalResp {
	return restaurantInternalResp{
		restaurantPublicResp: toRestaurantPublic(r),
		IsActive:             r.IsActive,
		ConfirmationURL:      r.ConfirmationURL,
		WebhookURL:           r.WebhookURL,
	}
}

type orderItemResp struct {
	MenuItemID uuid.UUID `json:"menu_item_id"`
	Name       string    `json:"name"`
	PriceCents int       `json:"price_cents"`
	Quantity   int       `json:"quantity"`
	TotalCents int       `json:"total_cents"`
}

type orderResp struct {
	ID                    uuid.UUID       `json:"id"`
	RestaurantID          uuid.UUID       `json:"restaurant_id"`
	Status                string          `json:"status"`
	SubtotalCents         int             `json:"subtotal_cents"`
	DeliveryFeeCents      int             `json:"delivery_fee_cents"`
	TotalCents            int             `json:"total_cents"`
	PaymentMethod         string          `json:"payment_method"`
	PaymentURL            *string         `json:"payment_url,omitempty"`
	ExpiresAt             *time.Time      `json:"expires_at,omitempty"`
	RestaurantConfirmedAt *time.Time      `json:"restaurant_confirmed_at,omitempty"`
	RestaurantExternalRef *string         `json:"restaurant_external_ref,omitempty"`
	PaidAt                *time.Time      `json:"paid_at,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
	Items                 []orderItemResp `json:"items"`
}

func toOrderResp(o domain.Order) orderResp {
	items := make([]orderItemResp, 0, len(o.Items))
	for _, i := range o.Items {
		items = append(items, orderItemResp{
			MenuItemID: i.MenuItemID,
			Name:       i.Name,
			PriceCents: i.PriceCents,
			Quantity:   i.Quantity,
			TotalCents: i.TotalCents,
		})
	}

	return orderResp{
		ID:                    o.ID,
		RestaurantID:          o.RestaurantID,
		Status:                o.Status,
		SubtotalCents:         o.SubtotalCents,
		DeliveryFeeCents:      o.DeliveryFeeCents,
		TotalCents:            o.TotalCents,
		PaymentMethod:         o.PaymentMethod,
		PaymentURL:            o.PaymentURL,
		ExpiresAt:             o.ExpiresAt,
		RestaurantConfirmedAt: o.RestaurantConfirmedAt,
		RestaurantExternalRef: o.RestaurantExternalRef,
		PaidAt:                o.PaidAt,
		CreatedAt:             o.CreatedAt,
		Items:                 items,
	}
}
