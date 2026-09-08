package service

import (
	"context"
	"net/http"

	"avitotask/internal/apperr"
	"avitotask/internal/applog"
	"avitotask/internal/domain"
	"avitotask/internal/store"

	"github.com/google/uuid"
)

type OrderRepo interface {
	ListRestaurants(ctx context.Context, q store.ListRestaurantsQuery) ([]domain.Restaurant, int, error)
	GetRestaurantPublic(ctx context.Context, id uuid.UUID) (domain.Restaurant, error)
	GetMenu(ctx context.Context, restaurantID uuid.UUID) ([]domain.Category, []domain.MenuItem, error)
	GetOrderByIdempotencyKey(ctx context.Context, key string) (*domain.Order, error)
	CreateOrderDraft(ctx context.Context, p store.CreateOrderParams) (store.Draft, error)
	RejectOrder(ctx context.Context, orderID uuid.UUID, status, reason string) error
	MarkOrderAwaitingPayment(ctx context.Context, orderID, paymentToken uuid.UUID, paymentURL, externalRef, comment string) error
	GetOrderByID(ctx context.Context, id uuid.UUID) (domain.Order, error)
	CancelOrderByUser(ctx context.Context, orderID uuid.UUID) error
	PayOrderByToken(ctx context.Context, token uuid.UUID) error
	RestaurantChangeStatus(ctx context.Context, restaurantID, orderID uuid.UUID, newStatus string, allowedFrom []string, reason string) error
	ListRestaurantOrders(ctx context.Context, restaurantID uuid.UUID, status string) ([]domain.Order, error)
	UpdateRestaurantMe(ctx context.Context, restaurantID uuid.UUID, p store.UpdateRestaurantParams) (domain.Restaurant, error)
	SyncMenu(ctx context.Context, restaurantID uuid.UUID, req store.SyncMenuRequest) error
}

type CreateOrderItem struct {
	MenuItemID uuid.UUID
	Quantity   int
}

type CreateOrderInput struct {
	RestaurantID    uuid.UUID
	ExternalUserID  string
	CustomerName    string
	CustomerPhone   string
	CustomerAddress string
	CustomerComment string
	PaymentMethod   string
	IdempotencyKey  string
	Items           []CreateOrderItem
}

type CreateOrderResult struct {
	Order    domain.Order
	Replayed bool
}

type RestaurantAction string

const (
	ActionPreparing  RestaurantAction = "preparing"
	ActionDelivering RestaurantAction = "delivering"
	ActionComplete   RestaurantAction = "completed"
	ActionCancel     RestaurantAction = "cancelled"
)

type OrderService struct {
	repo      OrderRepo
	confirmer OrderConfirmer
	baseURL   string
}

func NewOrderService(repo OrderRepo, confirmer OrderConfirmer, baseURL string) *OrderService {
	return &OrderService{repo: repo, confirmer: confirmer, baseURL: baseURL}
}

func (s *OrderService) SetBaseURL(url string) {
	s.baseURL = url
}

func (s *OrderService) ListRestaurants(ctx context.Context, q store.ListRestaurantsQuery) ([]domain.Restaurant, int, error) {
	return s.repo.ListRestaurants(ctx, q)
}

func (s *OrderService) GetRestaurant(ctx context.Context, id uuid.UUID) (domain.Restaurant, error) {
	return s.repo.GetRestaurantPublic(ctx, id)
}

func (s *OrderService) GetMenu(ctx context.Context, restaurantID uuid.UUID) ([]domain.Category, []domain.MenuItem, error) {
	return s.repo.GetMenu(ctx, restaurantID)
}

func (s *OrderService) GetOrder(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	return s.repo.GetOrderByID(ctx, id)
}

func (s *OrderService) ListRestaurantOrders(ctx context.Context, restaurantID uuid.UUID, status string) ([]domain.Order, error) {
	return s.repo.ListRestaurantOrders(ctx, restaurantID, status)
}

func (s *OrderService) UpdateRestaurantMe(ctx context.Context, restaurantID uuid.UUID, p store.UpdateRestaurantParams) (domain.Restaurant, error) {
	return s.repo.UpdateRestaurantMe(ctx, restaurantID, p)
}

func (s *OrderService) SyncMenu(ctx context.Context, restaurantID uuid.UUID, req store.SyncMenuRequest) error {
	return s.repo.SyncMenu(ctx, restaurantID, req)
}

func (s *OrderService) Pay(ctx context.Context, token uuid.UUID) error {
	if err := s.repo.PayOrderByToken(ctx, token); err != nil {
		return err
	}
	applog.Info("payment accepted", "payment_token", token.String())
	return nil
}

func (s *OrderService) CancelByUser(ctx context.Context, orderID uuid.UUID) error {
	if err := s.repo.CancelOrderByUser(ctx, orderID); err != nil {
		return err
	}
	applog.Info("order cancelled by user", "order_id", orderID.String())
	return nil
}

func (s *OrderService) RestaurantAction(ctx context.Context, restaurantID, orderID uuid.UUID, action RestaurantAction, reason string) error {
	if err := s.restaurantAction(ctx, restaurantID, orderID, action, reason); err != nil {
		return err
	}
	applog.Info("order status changed by restaurant",
		"order_id", orderID.String(),
		"restaurant_id", restaurantID.String(),
		"action", string(action),
	)
	return nil
}

func (s *OrderService) restaurantAction(ctx context.Context, restaurantID, orderID uuid.UUID, action RestaurantAction, reason string) error {
	switch action {
	case ActionPreparing:
		return s.repo.RestaurantChangeStatus(ctx, restaurantID, orderID, "preparing", []string{"paid"}, reason)
	case ActionDelivering:
		return s.repo.RestaurantChangeStatus(ctx, restaurantID, orderID, "delivering", []string{"preparing"}, reason)
	case ActionComplete:
		return s.repo.RestaurantChangeStatus(ctx, restaurantID, orderID, "completed", []string{"delivering"}, reason)
	case ActionCancel:
		return s.repo.RestaurantChangeStatus(ctx, restaurantID, orderID, "cancelled", []string{"paid", "preparing", "delivering"}, reason)
	}
	return apperr.BadRequest("unknown action")
}

// compensate откатывает резерв и переводит заказ в терминальный статус.
// Ошибка компенсации критична (риск «висящего» резерва) — логируется как Error.
func (s *OrderService) compensate(ctx context.Context, orderID uuid.UUID, status, reason string) {
	if err := s.repo.RejectOrder(ctx, orderID, status, reason); err != nil {
		applog.Error("failed to compensate order",
			"order_id", orderID.String(),
			"target_status", status,
			"reason", reason,
			"error", err.Error(),
		)
	}
}

func (s *OrderService) CreateOrder(ctx context.Context, in CreateOrderInput) (CreateOrderResult, error) {
	var res CreateOrderResult

	if in.RestaurantID == uuid.Nil {
		return res, apperr.BadRequest("restaurant_id is required")
	}
	if in.CustomerName == "" || in.CustomerPhone == "" || in.CustomerAddress == "" {
		return res, apperr.BadRequest("customer name, phone and address are required")
	}
	if len(in.Items) == 0 {
		return res, apperr.BadRequest("items are required")
	}
	for _, item := range in.Items {
		if item.MenuItemID == uuid.Nil || item.Quantity <= 0 {
			return res, apperr.BadRequest("invalid order items")
		}
	}
	if in.PaymentMethod == "" {
		in.PaymentMethod = "card"
	}

	if in.IdempotencyKey != "" {
		existing, err := s.repo.GetOrderByIdempotencyKey(ctx, in.IdempotencyKey)
		if err != nil {
			return res, err
		}
		if existing != nil {
			applog.Info("order replayed by idempotency key",
				"idempotency_key", in.IdempotencyKey,
				"order_id", existing.ID.String(),
			)
			return CreateOrderResult{Order: *existing, Replayed: true}, nil
		}
	}

	items := make([]store.OrderItemParams, 0, len(in.Items))
	for _, item := range in.Items {
		items = append(items, store.OrderItemParams{MenuItemID: item.MenuItemID, Quantity: item.Quantity})
	}

	draft, err := s.repo.CreateOrderDraft(ctx, store.CreateOrderParams{
		RestaurantID: in.RestaurantID,
		Customer: store.CustomerParams{
			ExternalUserID: strPtr(in.ExternalUserID),
			Name:           in.CustomerName,
			Phone:          in.CustomerPhone,
			Address:        in.CustomerAddress,
			Comment:        strPtr(in.CustomerComment),
		},
		Items:          items,
		PaymentMethod:  in.PaymentMethod,
		IdempotencyKey: strPtr(in.IdempotencyKey),
	})
	if err != nil {
		return res, err
	}

	applog.Info("order draft created",
		"order_id", draft.OrderID.String(),
		"restaurant_id", draft.RestaurantID.String(),
		"items", len(in.Items),
	)

	details := map[string]any{"order_id": draft.OrderID.String()}

	if draft.ConfirmationURL == nil || *draft.ConfirmationURL == "" {
		applog.Warn("restaurant has no confirmation url",
			"order_id", draft.OrderID.String(),
			"restaurant_id", draft.RestaurantID.String(),
		)
		s.compensate(ctx, draft.OrderID, "rejected_by_restaurant", "confirmation url is not configured")
		return res, apperr.WithDetails(
			apperr.New(http.StatusConflict, "CONFIRMATION_URL_MISSING", "restaurant confirmation url is not configured"),
			details,
		)
	}

	confirmItems := make([]ConfirmItem, 0, len(in.Items))
	for _, item := range in.Items {
		confirmItems = append(confirmItems, ConfirmItem{MenuItemID: item.MenuItemID, Quantity: item.Quantity})
	}

	secret := ""
	if draft.WebhookSecret != nil {
		secret = *draft.WebhookSecret
	}

	confirmation, err := s.confirmer.Confirm(ctx, ConfirmInput{
		OrderID:         draft.OrderID,
		RestaurantID:    draft.RestaurantID,
		ConfirmationURL: *draft.ConfirmationURL,
		TimeoutMS:       draft.ConfirmationTimeoutMS,
		WebhookSecret:   secret,
		ExpiresAt:       draft.ExpiresAt,
		PaymentMethod:   in.PaymentMethod,
		Items:           confirmItems,
	})
	if err != nil {
		applog.Warn("restaurant confirmation failed",
			"order_id", draft.OrderID.String(),
			"confirmation_url", *draft.ConfirmationURL,
			"error", err.Error(),
		)
		s.compensate(ctx, draft.OrderID, "expired", "confirmation timeout")
		return res, apperr.WithDetails(
			apperr.New(http.StatusConflict, "CONFIRMATION_TIMEOUT", "restaurant did not respond"),
			details,
		)
	}

	if !confirmation.Confirmed {
		applog.Warn("restaurant rejected order",
			"order_id", draft.OrderID.String(),
			"comment", confirmation.Comment,
		)
		s.compensate(ctx, draft.OrderID, "rejected_by_restaurant", confirmation.Comment)
		return res, apperr.WithDetails(
			apperr.New(http.StatusConflict, "RESTAURANT_REJECTED", "restaurant cannot fulfill the order"),
			details,
		)
	}

	applog.Info("restaurant confirmed order",
		"order_id", draft.OrderID.String(),
		"external_order_ref", confirmation.ExternalOrderRef,
	)

	paymentToken := uuid.New()
	paymentURL := s.baseURL + "/mock/pay/" + paymentToken.String()

	err = s.repo.MarkOrderAwaitingPayment(
		ctx,
		draft.OrderID,
		paymentToken,
		paymentURL,
		confirmation.ExternalOrderRef,
		confirmation.Comment,
	)
	if err != nil {
		return res, err
	}

	applog.Info("order moved to awaiting_payment",
		"order_id", draft.OrderID.String(),
		"payment_url", paymentURL,
	)

	order, err := s.repo.GetOrderByID(ctx, draft.OrderID)
	if err != nil {
		return res, err
	}

	return CreateOrderResult{Order: order}, nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
