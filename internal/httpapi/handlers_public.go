package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"avitotask/internal/apperr"
	"avitotask/internal/service"
	"avitotask/internal/store"

	"github.com/google/uuid"
)

type statusResponse struct {
	Status string `json:"status"`
}

func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

func (s *Server) ListRestaurants(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	page := 1
	pageSize := 20

	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}

	if v := q.Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			pageSize = n
		}
	}

	listQuery := store.ListRestaurantsQuery{
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}

	if v := q.Get("query"); v != "" {
		listQuery.Query = &v
	}
	if v := q.Get("category"); v != "" {
		listQuery.Category = &v
	}
	if v := q.Get("open_now"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			listQuery.OpenNow = &b
		}
	}

	items, total, err := s.Orders.ListRestaurants(r.Context(), listQuery)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := make([]restaurantPublicResp, 0, len(items))
	for _, item := range items {
		resp = append(resp, toRestaurantPublic(item))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     resp,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

func (s *Server) GetRestaurant(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("restaurantId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid restaurant id"))
		return
	}

	restaurant, err := s.Orders.GetRestaurant(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toRestaurantPublic(restaurant))
}

type menuItemResp struct {
	ID            uuid.UUID  `json:"id"`
	CategoryID    *uuid.UUID `json:"category_id"`
	ExternalID    *string    `json:"external_id"`
	Name          string     `json:"name"`
	Description   *string    `json:"description"`
	PriceCents    int        `json:"price_cents"`
	IsAvailable   bool       `json:"is_available"`
	StockQuantity *int       `json:"stock_quantity"`
	SortOrder     int        `json:"sort_order"`
}

type menuCategoryResp struct {
	ID         uuid.UUID      `json:"id"`
	ExternalID *string        `json:"external_id"`
	Name       string         `json:"name"`
	SortOrder  int            `json:"sort_order"`
	Items      []menuItemResp `json:"items"`
}

type menuResp struct {
	RestaurantID uuid.UUID          `json:"restaurant_id"`
	Categories   []menuCategoryResp `json:"categories"`
}

func (s *Server) GetRestaurantMenu(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := uuid.Parse(r.PathValue("restaurantId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid restaurant id"))
		return
	}

	categories, items, err := s.Orders.GetMenu(r.Context(), restaurantID)
	if err != nil {
		writeError(w, err)
		return
	}

	itemsByCategory := make(map[uuid.UUID][]menuItemResp)
	for _, item := range items {
		if item.CategoryID == nil {
			continue
		}
		itemsByCategory[*item.CategoryID] = append(itemsByCategory[*item.CategoryID], menuItemResp{
			ID:            item.ID,
			CategoryID:    item.CategoryID,
			ExternalID:    item.ExternalID,
			Name:          item.Name,
			Description:   item.Description,
			PriceCents:    item.PriceCents,
			IsAvailable:   item.IsAvailable,
			StockQuantity: item.StockQuantity,
			SortOrder:     item.SortOrder,
		})
	}

	respCategories := make([]menuCategoryResp, 0, len(categories))
	for _, category := range categories {
		respCategories = append(respCategories, menuCategoryResp{
			ID:         category.ID,
			ExternalID: category.ExternalID,
			Name:       category.Name,
			SortOrder:  category.SortOrder,
			Items:      itemsByCategory[category.ID],
		})
	}

	writeJSON(w, http.StatusOK, menuResp{
		RestaurantID: restaurantID,
		Categories:   respCategories,
	})
}

type customerInput struct {
	ExternalUserID string `json:"external_user_id"`
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	Address        string `json:"address"`
	Comment        string `json:"comment"`
}

type orderItemInput struct {
	MenuItemID uuid.UUID `json:"menu_item_id"`
	Quantity   int       `json:"quantity"`
}

type createOrderRequest struct {
	RestaurantID  uuid.UUID        `json:"restaurant_id"`
	Customer      customerInput    `json:"customer"`
	Items         []orderItemInput `json:"items"`
	PaymentMethod string           `json:"payment_method"`
}

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, apperr.BadRequest("invalid request body"))
		return
	}

	in := service.CreateOrderInput{
		RestaurantID:    req.RestaurantID,
		ExternalUserID:  req.Customer.ExternalUserID,
		CustomerName:    req.Customer.Name,
		CustomerPhone:   req.Customer.Phone,
		CustomerAddress: req.Customer.Address,
		CustomerComment: req.Customer.Comment,
		PaymentMethod:   req.PaymentMethod,
		IdempotencyKey:  r.Header.Get("Idempotency-Key"),
	}
	for _, item := range req.Items {
		in.Items = append(in.Items, service.CreateOrderItem{
			MenuItemID: item.MenuItemID,
			Quantity:   item.Quantity,
		})
	}

	result, err := s.Orders.CreateOrder(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}

	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}

	writeJSON(w, status, toOrderResp(result.Order))
}

func (s *Server) GetOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(r.PathValue("orderId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid order id"))
		return
	}

	order, err := s.Orders.GetOrder(r.Context(), orderID)
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toOrderResp(order))
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request) {
	orderID, err := uuid.Parse(r.PathValue("orderId"))
	if err != nil {
		writeError(w, apperr.BadRequest("invalid order id"))
		return
	}

	if err := s.Orders.CancelByUser(r.Context(), orderID); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{Status: "cancelled"})
}
