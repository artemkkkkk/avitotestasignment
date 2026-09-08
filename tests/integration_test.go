package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"avitotask/internal/httpapi"
	"avitotask/internal/service"
	"avitotask/internal/store"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testAdminToken = "test-admin-token"

type confirmStub struct {
	mu        sync.Mutex
	confirmed bool
}

func (c *confirmStub) set(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.confirmed = v
}

func (c *confirmStub) get() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confirmed
}

type env struct {
	t            *testing.T
	ctx          context.Context
	pool         *pgxpool.Pool
	st           *store.Store
	ts           *httptest.Server
	confirm      *confirmStub
	restaurantID uuid.UUID
	apiKey       string
}

func setup(t *testing.T) *env {
	t.Helper()

	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN is not set")
	}

	ctx := context.Background()

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	applyMigrations(t, ctx, pool)

	st := store.New(pool)

	confirm := &confirmStub{confirmed: true}
	confirmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if confirm.get() {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"confirmed":          true,
				"external_order_ref": "ref-1",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"confirmed": false,
			"comment":   "no ingredients",
		})
	}))
	t.Cleanup(confirmSrv.Close)

	orderSvc := service.NewOrderService(st, service.NewHTTPConfirmer(), "")
	srv := httpapi.NewServer(st, orderSvc, "")
	srv.AdminToken = testAdminToken

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	srv.BaseURL = ts.URL
	orderSvc.SetBaseURL(ts.URL)

	restaurant, err := st.CreateRestaurant(ctx, store.CreateRestaurantParams{
		Name:             "Test Pizza",
		Category:         "pizza",
		Address:          "Test street 1",
		Phone:            "+70000000000",
		DeliveryFeeCents: 100,
		MinOrderCents:    0,
		ConfirmationURL:  confirmSrv.URL,
		WebhookURL:       confirmSrv.URL + "/webhook",
		WebhookSecret:    "secret",
		IsActive:         true,
		IsOpen:           true,
	})
	if err != nil {
		t.Fatalf("create restaurant: %v", err)
	}

	_, apiKey, err := st.CreateAPIKey(ctx, restaurant.ID, "test-key")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	stock := 5
	zero := 0
	err = st.SyncMenu(ctx, restaurant.ID, store.SyncMenuRequest{
		Categories: []store.SyncCategory{
			{
				ExternalID: "main",
				Name:       "Main",
				SortOrder:  1,
				Items: []store.SyncItem{
					{ExternalID: "burger", Name: "Burger", PriceCents: 500, StockQuantity: &stock, SortOrder: 1},
					{ExternalID: "soldout", Name: "Sold out", PriceCents: 600, StockQuantity: &zero, SortOrder: 2},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("sync menu: %v", err)
	}

	return &env{
		t:            t,
		ctx:          ctx,
		pool:         pool,
		st:           st,
		ts:           ts,
		confirm:      confirm,
		restaurantID: restaurant.ID,
		apiKey:       apiKey,
	}
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	entries, err := os.ReadDir("../migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	names := make([]string, 0)
	for _, en := range entries {
		if !en.IsDir() && strings.HasSuffix(en.Name(), ".up.sql") {
			names = append(names, en.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("../migrations", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}

func (e *env) do(method, path, bearer string, body any) (int, map[string]any) {
	e.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(e.ctx, method, e.ts.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) doAdmin(method, path string, body any) (int, map[string]any) {
	e.t.Helper()

	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(e.ctx, method, e.ts.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Admin-Token", testAdminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) itemID(externalID string) uuid.UUID {
	e.t.Helper()

	_, items, err := e.st.GetMenu(e.ctx, e.restaurantID)
	if err != nil {
		e.t.Fatalf("get menu: %v", err)
	}
	for _, i := range items {
		if i.ExternalID != nil && *i.ExternalID == externalID {
			return i.ID
		}
	}
	e.t.Fatalf("item %s not found", externalID)
	return uuid.Nil
}

func (e *env) stockOf(externalID string) *int {
	e.t.Helper()

	_, items, err := e.st.GetMenu(e.ctx, e.restaurantID)
	if err != nil {
		e.t.Fatalf("get menu: %v", err)
	}
	for _, i := range items {
		if i.ExternalID != nil && *i.ExternalID == externalID {
			return i.StockQuantity
		}
	}
	e.t.Fatalf("item %s not found", externalID)
	return nil
}

func (e *env) createOrder(itemExternalID string, qty int) (int, map[string]any) {
	e.t.Helper()

	body := map[string]any{
		"restaurant_id": e.restaurantID,
		"customer": map[string]any{
			"name":    "Ivan",
			"phone":   "+79990000000",
			"address": "Addr 1",
		},
		"items": []map[string]any{
			{"menu_item_id": e.itemID(itemExternalID), "quantity": qty},
		},
		"payment_method": "card",
	}

	return e.do(http.MethodPost, "/api/v1/orders", "", body)
}

func (e *env) pay(order map[string]any) (int, map[string]any) {
	e.t.Helper()

	pu, _ := order["payment_url"].(string)
	if pu == "" {
		e.t.Fatalf("payment_url is empty")
	}

	token := strings.TrimPrefix(pu, e.ts.URL+"/mock/pay/")
	return e.do(http.MethodPost, "/mock/pay/"+token+"/pay", "", nil)
}

func errorCode(body map[string]any) string {
	errObj, _ := body["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

func TestLocalStockRejection(t *testing.T) {
	e := setup(t)

	code, body := e.createOrder("burger", 10)
	if code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", code)
	}
	if errorCode(body) != "ITEMS_UNAVAILABLE" {
		t.Fatalf("expected ITEMS_UNAVAILABLE, got %s", errorCode(body))
	}

	code, body = e.createOrder("soldout", 1)
	if code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", code)
	}
	if errorCode(body) != "ITEMS_UNAVAILABLE" {
		t.Fatalf("expected ITEMS_UNAVAILABLE, got %s", errorCode(body))
	}

	if stock := e.stockOf("burger"); stock == nil || *stock != 5 {
		t.Fatalf("stock must remain 5 after rejected order")
	}
}

func TestRestaurantRejection(t *testing.T) {
	e := setup(t)
	e.confirm.set(false)

	code, body := e.createOrder("burger", 1)
	if code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", code)
	}
	if errorCode(body) != "RESTAURANT_REJECTED" {
		t.Fatalf("expected RESTAURANT_REJECTED, got %s", errorCode(body))
	}

	errObj, _ := body["error"].(map[string]any)
	details, _ := errObj["details"].(map[string]any)
	orderIDStr, _ := details["order_id"].(string)
	orderID, err := uuid.Parse(orderIDStr)
	if err != nil {
		t.Fatalf("parse order id: %v", err)
	}

	order, err := e.st.GetOrderByID(e.ctx, orderID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if order.Status != "rejected_by_restaurant" {
		t.Fatalf("expected rejected_by_restaurant, got %s", order.Status)
	}

	if stock := e.stockOf("burger"); stock == nil || *stock != 5 {
		t.Fatalf("stock must be restored to 5, got %v", stock)
	}
}

func TestPaymentFlow(t *testing.T) {
	e := setup(t)

	code, order := e.createOrder("burger", 2)
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	if order["status"] != "awaiting_payment" {
		t.Fatalf("expected awaiting_payment, got %v", order["status"])
	}
	if order["payment_url"] == nil {
		t.Fatalf("payment_url is missing")
	}

	code, _ = e.pay(order)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on pay, got %d", code)
	}

	orderID, _ := order["id"].(string)

	code, got := e.do(http.MethodGet, "/api/v1/orders/"+orderID, "", nil)
	if code != http.StatusOK || got["status"] != "paid" {
		t.Fatalf("expected paid, got %v", got["status"])
	}

	for _, step := range []string{"preparing", "delivering", "complete"} {
		code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+orderID+"/"+step, e.apiKey, nil)
		if code != http.StatusOK {
			t.Fatalf("step %s: expected 200, got %d", step, code)
		}
	}

	code, got = e.do(http.MethodGet, "/api/v1/orders/"+orderID, "", nil)
	if got["status"] != "completed" {
		t.Fatalf("expected completed, got %v", got["status"])
	}

	if stock := e.stockOf("burger"); stock == nil || *stock != 3 {
		t.Fatalf("stock must be 3 after paid order, got %v", stock)
	}
}

func TestPaymentExpiration(t *testing.T) {
	e := setup(t)

	code, order := e.createOrder("burger", 1)
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}

	orderID, _ := order["id"].(string)

	_, err := e.pool.Exec(
		e.ctx,
		`UPDATE orders SET expires_at = now() - interval '1 minute' WHERE id = $1`,
		orderID,
	)
	if err != nil {
		t.Fatalf("force expire: %v", err)
	}

	if err := e.st.ExpireOrders(e.ctx); err != nil {
		t.Fatalf("expire orders: %v", err)
	}

	code, got := e.do(http.MethodGet, "/api/v1/orders/"+orderID, "", nil)
	if got["status"] != "expired" {
		t.Fatalf("expected expired, got %v", got["status"])
	}

	code, _ = e.pay(order)
	if code != http.StatusConflict {
		t.Fatalf("expected 409 on pay after expiration, got %d", code)
	}

	if stock := e.stockOf("burger"); stock == nil || *stock != 5 {
		t.Fatalf("stock must be restored to 5, got %v", stock)
	}
}

func TestRestaurantStatusTransitions(t *testing.T) {
	e := setup(t)

	code, order := e.createOrder("burger", 1)
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	code, _ = e.pay(order)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on pay, got %d", code)
	}
	orderID, _ := order["id"].(string)

	code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+orderID+"/complete", e.apiKey, nil)
	if code != http.StatusConflict {
		t.Fatalf("expected 409 for paid->completed, got %d", code)
	}

	code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+orderID+"/preparing", e.apiKey, nil)
	if code != http.StatusOK {
		t.Fatalf("preparing: expected 200, got %d", code)
	}

	code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+orderID+"/preparing", e.apiKey, nil)
	if code != http.StatusConflict {
		t.Fatalf("expected 409 for preparing->preparing, got %d", code)
	}

	code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+orderID+"/delivering", e.apiKey, nil)
	if code != http.StatusOK {
		t.Fatalf("delivering: expected 200, got %d", code)
	}

	code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+orderID+"/complete", e.apiKey, nil)
	if code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d", code)
	}

	code, order2 := e.createOrder("burger", 1)
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	code, _ = e.pay(order2)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on pay, got %d", code)
	}
	order2ID, _ := order2["id"].(string)

	code, _ = e.do(http.MethodPost, "/api/v1/restaurant/orders/"+order2ID+"/cancel", e.apiKey, map[string]any{
		"reason": "out of ingredients",
	})
	if code != http.StatusOK {
		t.Fatalf("cancel: expected 200, got %d", code)
	}

	code, got := e.do(http.MethodGet, "/api/v1/orders/"+order2ID, "", nil)
	if got["status"] != "cancelled" {
		t.Fatalf("expected cancelled, got %v", got["status"])
	}

	if stock := e.stockOf("burger"); stock == nil || *stock != 4 {
		t.Fatalf("stock must be 4, got %v", stock)
	}
}

func TestAdminAPI(t *testing.T) {
	e := setup(t)

	code, _ := e.do(http.MethodPost, "/api/v1/admin/restaurants", "", map[string]any{"name": "X"})
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without admin token, got %d", code)
	}

	code, body := e.doAdmin(http.MethodPost, "/api/v1/admin/restaurants", map[string]any{
		"name":               "Admin Cafe",
		"category":           "cafe",
		"delivery_fee_cents": 0,
		"min_order_cents":    0,
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}

	rest, _ := body["restaurant"].(map[string]any)
	restID, _ := rest["id"].(string)
	firstKey, _ := body["api_key"].(string)
	firstKeyID, _ := body["api_key_id"].(string)

	if firstKey == "" || restID == "" {
		t.Fatalf("api_key or restaurant id is missing")
	}

	code, me := e.do(http.MethodGet, "/api/v1/restaurant/me", firstKey, nil)
	if code != http.StatusOK || me["name"] != "Admin Cafe" {
		t.Fatalf("expected 200 and Admin Cafe, got %d %v", code, me["name"])
	}

	code, body2 := e.doAdmin(http.MethodPost, "/api/v1/admin/restaurants/"+restID+"/api-keys", map[string]any{
		"name": "second",
	})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", code)
	}
	secondKey, _ := body2["api_key"].(string)

	code, _ = e.doAdmin(http.MethodPost, "/api/v1/admin/api-keys/"+firstKeyID+"/revoke", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on revoke, got %d", code)
	}

	code, _ = e.do(http.MethodGet, "/api/v1/restaurant/me", firstKey, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked key, got %d", code)
	}

	code, _ = e.do(http.MethodGet, "/api/v1/restaurant/me", secondKey, nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 for second key, got %d", code)
	}
}
