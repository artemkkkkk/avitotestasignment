package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"avitotask/internal/apperr"
	"avitotask/internal/domain"
	"avitotask/internal/hmacsign"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const restaurantColumns = `
	id,
	external_id,
	name,
	description,
	category,
	address,
	phone,
	delivery_fee_cents,
	min_order_cents,
	estimated_delivery_minutes,
	is_active,
	is_open,
	confirmation_url,
	confirmation_timeout_ms,
	webhook_url,
	webhook_secret,
	created_at,
	updated_at
`

const orderColumns = `
	id,
	restaurant_id,
	customer_id,
	status,
	subtotal_cents,
	delivery_fee_cents,
	total_cents,
	payment_method,
	payment_token,
	payment_url,
	expires_at,
	restaurant_confirmed_at,
	restaurant_external_ref,
	paid_at,
	customer_name,
	customer_phone,
	customer_address,
	customer_comment,
	created_at
`

type rowScanner interface {
	Scan(dest ...any) error
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func scanRestaurant(rs rowScanner) (domain.Restaurant, error) {
	var r domain.Restaurant
	err := rs.Scan(
		&r.ID,
		&r.ExternalID,
		&r.Name,
		&r.Description,
		&r.Category,
		&r.Address,
		&r.Phone,
		&r.DeliveryFeeCents,
		&r.MinOrderCents,
		&r.EstimatedDeliveryMinutes,
		&r.IsActive,
		&r.IsOpen,
		&r.ConfirmationURL,
		&r.ConfirmationTimeoutMS,
		&r.WebhookURL,
		&r.WebhookSecret,
		&r.CreatedAt,
		&r.UpdatedAt,
	)
	return r, err
}

func scanOrder(rs rowScanner) (domain.Order, error) {
	var o domain.Order
	err := rs.Scan(
		&o.ID,
		&o.RestaurantID,
		&o.CustomerID,
		&o.Status,
		&o.SubtotalCents,
		&o.DeliveryFeeCents,
		&o.TotalCents,
		&o.PaymentMethod,
		&o.PaymentToken,
		&o.PaymentURL,
		&o.ExpiresAt,
		&o.RestaurantConfirmedAt,
		&o.RestaurantExternalRef,
		&o.PaidAt,
		&o.CustomerName,
		&o.CustomerPhone,
		&o.CustomerAddress,
		&o.CustomerComment,
		&o.CreatedAt,
	)
	return o, err
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

type SeedDemoParams struct {
	ExternalID       string
	Name             string
	Description      string
	Category         string
	Address          string
	Phone            string
	APIKey           string
	WebhookSecret    string
	ConfirmationURL  string
	WebhookURL       string
	DeliveryFeeCents int
	MinOrderCents    int
}

func (s *Store) SeedDemo(ctx context.Context, p SeedDemoParams) error {
	var restaurantID uuid.UUID

	err := s.pool.QueryRow(
		ctx,
		`
		INSERT INTO restaurants (
			external_id,
			name,
			description,
			category,
			address,
			phone,
			delivery_fee_cents,
			min_order_cents,
			is_active,
			is_open,
			confirmation_url,
			webhook_url,
			webhook_secret,
			confirmation_timeout_ms
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, true, true, $9, $10, $11, 5000)
		ON CONFLICT (external_id) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			category = EXCLUDED.category,
			address = EXCLUDED.address,
			phone = EXCLUDED.phone,
			delivery_fee_cents = EXCLUDED.delivery_fee_cents,
			min_order_cents = EXCLUDED.min_order_cents,
			is_active = true,
			is_open = true,
			confirmation_url = EXCLUDED.confirmation_url,
			webhook_url = EXCLUDED.webhook_url,
			webhook_secret = EXCLUDED.webhook_secret,
			updated_at = now()
		RETURNING id
		`,
		p.ExternalID,
		p.Name,
		nullString(p.Description),
		nullString(p.Category),
		nullString(p.Address),
		nullString(p.Phone),
		p.DeliveryFeeCents,
		p.MinOrderCents,
		nullString(p.ConfirmationURL),
		nullString(p.WebhookURL),
		nullString(p.WebhookSecret),
	).Scan(&restaurantID)
	if err != nil {
		return fmt.Errorf("seed restaurant: %w", err)
	}

	keyHash := hashAPIKey(p.APIKey)
	keyPrefix := p.APIKey
	if len(keyPrefix) > 8 {
		keyPrefix = keyPrefix[:8]
	}

	_, err = s.pool.Exec(
		ctx,
		`
		INSERT INTO restaurant_api_keys (restaurant_id, name, key_hash, key_prefix, is_active)
		VALUES ($1, 'demo', $2, $3, true)
		ON CONFLICT (key_hash) DO NOTHING
		`,
		restaurantID,
		keyHash,
		keyPrefix,
	)
	if err != nil {
		return fmt.Errorf("seed api key: %w", err)
	}

	return nil
}

type ListRestaurantsQuery struct {
	Query    *string
	Category *string
	OpenNow  *bool
	Limit    int
	Offset   int
}

func (s *Store) ListRestaurants(ctx context.Context, q ListRestaurantsQuery) ([]domain.Restaurant, int, error) {
	where := `
		WHERE r.is_active = true
		AND ($1::text IS NULL OR r.name ILIKE '%' || $1 || '%')
		AND ($2::text IS NULL OR r.category = $2)
		AND ($3::boolean IS NULL OR r.is_open = $3)
	`

	var total int
	err := s.pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM restaurants r `+where,
		q.Query,
		q.Category,
		q.OpenNow,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count restaurants: %w", err)
	}

	rows, err := s.pool.Query(
		ctx,
		`
		SELECT `+restaurantColumns+`
		FROM restaurants r
		`+where+`
		ORDER BY r.name
		LIMIT $4 OFFSET $5
		`,
		q.Query,
		q.Category,
		q.OpenNow,
		q.Limit,
		q.Offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list restaurants: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Restaurant, 0, q.Limit)
	for rows.Next() {
		r, err := scanRestaurant(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan restaurant: %w", err)
		}
		items = append(items, r)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("rows restaurants: %w", err)
	}

	return items, total, nil
}

func (s *Store) GetRestaurantPublic(ctx context.Context, id uuid.UUID) (domain.Restaurant, error) {
	row := s.pool.QueryRow(
		ctx,
		`
		SELECT `+restaurantColumns+`
		FROM restaurants
		WHERE id = $1 AND is_active = true
		`,
		id,
	)

	r, err := scanRestaurant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, apperr.NotFound("restaurant not found")
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("get restaurant: %w", err)
	}

	return r, nil
}

func (s *Store) GetMenu(ctx context.Context, restaurantID uuid.UUID) ([]domain.Category, []domain.MenuItem, error) {
	catRows, err := s.pool.Query(
		ctx,
		`
		SELECT id, restaurant_id, external_id, name, sort_order, is_active
		FROM menu_categories
		WHERE restaurant_id = $1 AND is_active = true
		ORDER BY sort_order
		`,
		restaurantID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query categories: %w", err)
	}
	defer catRows.Close()

	categories := make([]domain.Category, 0)
	for catRows.Next() {
		var c domain.Category
		if err := catRows.Scan(&c.ID, &c.RestaurantID, &c.ExternalID, &c.Name, &c.SortOrder, &c.IsActive); err != nil {
			return nil, nil, fmt.Errorf("scan category: %w", err)
		}
		categories = append(categories, c)
	}
	if err := catRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("category rows: %w", err)
	}

	itemRows, err := s.pool.Query(
		ctx,
		`
		SELECT
			id,
			restaurant_id,
			category_id,
			external_id,
			name,
			description,
			price_cents,
			image_url,
			is_available,
			stock_quantity,
			sort_order
		FROM menu_items
		WHERE restaurant_id = $1
		ORDER BY sort_order
		`,
		restaurantID,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("query items: %w", err)
	}
	defer itemRows.Close()

	items := make([]domain.MenuItem, 0)
	for itemRows.Next() {
		var i domain.MenuItem
		if err := itemRows.Scan(
			&i.ID,
			&i.RestaurantID,
			&i.CategoryID,
			&i.ExternalID,
			&i.Name,
			&i.Description,
			&i.PriceCents,
			&i.ImageURL,
			&i.IsAvailable,
			&i.StockQuantity,
			&i.SortOrder,
		); err != nil {
			return nil, nil, fmt.Errorf("scan item: %w", err)
		}
		items = append(items, i)
	}
	if err := itemRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("item rows: %w", err)
	}

	return categories, items, nil
}

func (s *Store) GetRestaurantByAPIKeyHash(ctx context.Context, keyHash string) (domain.Restaurant, error) {
	row := s.pool.QueryRow(
		ctx,
		`
		SELECT
			r.id,
			r.external_id,
			r.name,
			r.description,
			r.category,
			r.address,
			r.phone,
			r.delivery_fee_cents,
			r.min_order_cents,
			r.estimated_delivery_minutes,
			r.is_active,
			r.is_open,
			r.confirmation_url,
			r.confirmation_timeout_ms,
			r.webhook_url,
			r.webhook_secret,
			r.created_at,
			r.updated_at
		FROM restaurants r
		JOIN restaurant_api_keys k ON k.restaurant_id = r.id
		WHERE k.key_hash = $1
		  AND k.is_active = true
		  AND r.is_active = true
		`,
		keyHash,
	)

	r, err := scanRestaurant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, apperr.Unauthorized("invalid api key")
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("get restaurant by api key: %w", err)
	}

	_, _ = s.pool.Exec(
		ctx,
		`
		UPDATE restaurant_api_keys
		SET last_used_at = now()
		WHERE key_hash = $1
		`,
		keyHash,
	)

	return r, nil
}

type UpdateRestaurantParams struct {
	Name             *string
	Description      *string
	Address          *string
	Phone            *string
	DeliveryFeeCents *int
	MinOrderCents    *int
	IsOpen           *bool
	ConfirmationURL  *string
	WebhookURL       *string
}

func (s *Store) UpdateRestaurantMe(ctx context.Context, restaurantID uuid.UUID, p UpdateRestaurantParams) (domain.Restaurant, error) {
	row := s.pool.QueryRow(
		ctx,
		`
		UPDATE restaurants SET
			name = COALESCE($2, name),
			description = COALESCE($3, description),
			address = COALESCE($4, address),
			phone = COALESCE($5, phone),
			delivery_fee_cents = COALESCE($6, delivery_fee_cents),
			min_order_cents = COALESCE($7, min_order_cents),
			is_open = COALESCE($8, is_open),
			confirmation_url = COALESCE($9, confirmation_url),
			webhook_url = COALESCE($10, webhook_url),
			updated_at = now()
		WHERE id = $1
		RETURNING `+restaurantColumns+`
		`,
		restaurantID,
		p.Name,
		p.Description,
		p.Address,
		p.Phone,
		p.DeliveryFeeCents,
		p.MinOrderCents,
		p.IsOpen,
		p.ConfirmationURL,
		p.WebhookURL,
	)

	r, err := scanRestaurant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, apperr.NotFound("restaurant not found")
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("update restaurant: %w", err)
	}

	return r, nil
}

type SyncCategory struct {
	ExternalID string     `json:"external_id"`
	Name       string     `json:"name"`
	SortOrder  int        `json:"sort_order"`
	Items      []SyncItem `json:"items"`
}

type SyncItem struct {
	ExternalID    string `json:"external_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	PriceCents    int    `json:"price_cents"`
	IsAvailable   *bool  `json:"is_available"`
	StockQuantity *int   `json:"stock_quantity"`
	SortOrder     int    `json:"sort_order"`
}

type SyncMenuRequest struct {
	Categories []SyncCategory `json:"categories"`
}

func (s *Store) SyncMenu(ctx context.Context, restaurantID uuid.UUID, req SyncMenuRequest) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin sync menu tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, category := range req.Categories {
		if category.ExternalID == "" {
			return apperr.BadRequest("category external_id is required")
		}

		var categoryID uuid.UUID
		err := tx.QueryRow(
			ctx,
			`
			INSERT INTO menu_categories (restaurant_id, external_id, name, sort_order, is_active)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (restaurant_id, external_id) DO UPDATE SET
				name = EXCLUDED.name,
				sort_order = EXCLUDED.sort_order,
				is_active = true,
				updated_at = now()
			RETURNING id
			`,
			restaurantID,
			category.ExternalID,
			category.Name,
			category.SortOrder,
		).Scan(&categoryID)
		if err != nil {
			return fmt.Errorf("upsert category: %w", err)
		}

		for _, item := range category.Items {
			if item.ExternalID == "" {
				return apperr.BadRequest("item external_id is required")
			}

			isAvailable := true
			if item.IsAvailable != nil {
				isAvailable = *item.IsAvailable
			}

			_, err := tx.Exec(
				ctx,
				`
				INSERT INTO menu_items (
					restaurant_id,
					category_id,
					external_id,
					name,
					description,
					price_cents,
					is_available,
					stock_quantity,
					sort_order
				)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (restaurant_id, external_id) DO UPDATE SET
					category_id = EXCLUDED.category_id,
					name = EXCLUDED.name,
					description = EXCLUDED.description,
					price_cents = EXCLUDED.price_cents,
					is_available = EXCLUDED.is_available,
					stock_quantity = EXCLUDED.stock_quantity,
					sort_order = EXCLUDED.sort_order,
					updated_at = now()
				`,
				restaurantID,
				categoryID,
				item.ExternalID,
				item.Name,
				nullString(item.Description),
				item.PriceCents,
				isAvailable,
				item.StockQuantity,
				item.SortOrder,
			)
			if err != nil {
				return fmt.Errorf("upsert item: %w", err)
			}
		}
	}

	return tx.Commit(ctx)
}

type CustomerParams struct {
	ExternalUserID *string
	Name           string
	Phone          string
	Address        string
	Comment        *string
}

type OrderItemParams struct {
	MenuItemID uuid.UUID `json:"menu_item_id"`
	Quantity   int       `json:"quantity"`
}

type CreateOrderParams struct {
	RestaurantID   uuid.UUID
	Customer       CustomerParams
	Items          []OrderItemParams
	PaymentMethod  string
	IdempotencyKey *string
}

type Draft struct {
	OrderID               uuid.UUID
	RestaurantID          uuid.UUID
	ConfirmationURL       *string
	ConfirmationTimeoutMS int
	WebhookSecret         *string
	ExpiresAt             time.Time
}

func (s *Store) GetOrderByIdempotencyKey(ctx context.Context, key string) (*domain.Order, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(
		ctx,
		`SELECT id FROM orders WHERE idempotency_key = $1`,
		key,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get order by idempotency key: %w", err)
	}

	o, err := s.GetOrderByID(ctx, id)
	if err != nil {
		return nil, err
	}

	return &o, nil
}

func (s *Store) CreateOrderDraft(ctx context.Context, p CreateOrderParams) (Draft, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Draft{}, fmt.Errorf("begin order tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var confirmationURL *string
	var confirmationTimeoutMS int
	var webhookSecret *string
	var deliveryFeeCents int
	var minOrderCents int
	var isOpen bool

	err = tx.QueryRow(
		ctx,
		`
		SELECT
			confirmation_url,
			confirmation_timeout_ms,
			webhook_secret,
			delivery_fee_cents,
			min_order_cents,
			is_open
		FROM restaurants
		WHERE id = $1 AND is_active = true
		FOR UPDATE
		`,
		p.RestaurantID,
	).Scan(
		&confirmationURL,
		&confirmationTimeoutMS,
		&webhookSecret,
		&deliveryFeeCents,
		&minOrderCents,
		&isOpen,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, apperr.NotFound("restaurant not found")
	}
	if err != nil {
		return Draft{}, fmt.Errorf("lock restaurant: %w", err)
	}

	if !isOpen {
		return Draft{}, apperr.New(http.StatusConflict, "RESTAURANT_CLOSED", "restaurant is closed")
	}

	if len(p.Items) == 0 {
		return Draft{}, apperr.BadRequest("items are required")
	}

	quantityByID := make(map[uuid.UUID]int)
	ids := make([]uuid.UUID, 0, len(p.Items))

	for _, item := range p.Items {
		if item.Quantity <= 0 {
			return Draft{}, apperr.BadRequest("quantity must be greater than zero")
		}
		if _, ok := quantityByID[item.MenuItemID]; !ok {
			ids = append(ids, item.MenuItemID)
		}
		quantityByID[item.MenuItemID] += item.Quantity
	}

	type itemRow struct {
		name      string
		price     int
		available bool
		stock     *int
	}

	rows, err := tx.Query(
		ctx,
		`
		SELECT id, name, price_cents, is_available, stock_quantity
		FROM menu_items
		WHERE restaurant_id = $1
		ORDER BY id
		FOR UPDATE
		`,
		p.RestaurantID,
	)
	if err != nil {
		return Draft{}, fmt.Errorf("lock menu items: %w", err)
	}

	fetched := make(map[uuid.UUID]itemRow)
	for rows.Next() {
		var id uuid.UUID
		var row itemRow
		if err := rows.Scan(&id, &row.name, &row.price, &row.available, &row.stock); err != nil {
			rows.Close()
			return Draft{}, fmt.Errorf("scan menu item: %w", err)
		}
		fetched[id] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Draft{}, fmt.Errorf("menu item rows: %w", err)
	}
	rows.Close()

	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := fetched[id]; !ok {
			missing = append(missing, id.String())
		}
	}
	if len(missing) > 0 {
		return Draft{}, apperr.WithDetails(
			apperr.New(http.StatusConflict, "ITEMS_UNAVAILABLE", "some items were not found in this restaurant"),
			map[string]any{"missing_item_ids": missing},
		)
	}

	type snapshot struct {
		id       uuid.UUID
		name     string
		price    int
		quantity int
		total    int
	}

	subtotal := 0
	snapshots := make([]snapshot, 0, len(quantityByID))

	for id, qty := range quantityByID {
		row := fetched[id]
		if !row.available {
			return Draft{}, apperr.WithDetails(
				apperr.New(http.StatusConflict, "ITEMS_UNAVAILABLE", "item is not available"),
				map[string]any{"menu_item_id": id.String()},
			)
		}
		if row.stock != nil && *row.stock < qty {
			return Draft{}, apperr.WithDetails(
				apperr.New(http.StatusConflict, "ITEMS_UNAVAILABLE", "not enough stock"),
				map[string]any{"menu_item_id": id.String()},
			)
		}

		total := row.price * qty
		subtotal += total

		snapshots = append(snapshots, snapshot{
			id:       id,
			name:     row.name,
			price:    row.price,
			quantity: qty,
			total:    total,
		})
	}

	if subtotal < minOrderCents {
		return Draft{}, apperr.New(http.StatusConflict, "MIN_ORDER_NOT_REACHED", "minimum order amount was not reached")
	}

	var customerID uuid.UUID
	if p.Customer.ExternalUserID != nil && *p.Customer.ExternalUserID != "" {
		err = tx.QueryRow(
			ctx,
			`
			INSERT INTO customers (external_user_id, phone, full_name)
			VALUES ($1, $2, $3)
			ON CONFLICT (external_user_id) DO UPDATE SET
				phone = EXCLUDED.phone,
				full_name = EXCLUDED.full_name,
				updated_at = now()
			RETURNING id
			`,
			p.Customer.ExternalUserID,
			nullString(p.Customer.Phone),
			nullString(p.Customer.Name),
		).Scan(&customerID)
	} else {
		err = tx.QueryRow(
			ctx,
			`
			INSERT INTO customers (phone, full_name)
			VALUES ($1, $2)
			RETURNING id
			`,
			nullString(p.Customer.Phone),
			nullString(p.Customer.Name),
		).Scan(&customerID)
	}
	if err != nil {
		return Draft{}, fmt.Errorf("upsert customer: %w", err)
	}

	expiresAt := time.Now().Add(60 * time.Second)
	total := subtotal + deliveryFeeCents

	var orderID uuid.UUID
	err = tx.QueryRow(
		ctx,
		`
		INSERT INTO orders (
			restaurant_id,
			customer_id,
			status,
			subtotal_cents,
			delivery_fee_cents,
			total_cents,
			payment_method,
			customer_name,
			customer_phone,
			customer_address,
			customer_comment,
			idempotency_key,
			expires_at
		)
		VALUES ($1, $2, 'pending_confirmation', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id
		`,
		p.RestaurantID,
		customerID,
		subtotal,
		deliveryFeeCents,
		total,
		p.PaymentMethod,
		p.Customer.Name,
		p.Customer.Phone,
		p.Customer.Address,
		p.Customer.Comment,
		p.IdempotencyKey,
		expiresAt,
	).Scan(&orderID)
	if err != nil {
		return Draft{}, fmt.Errorf("insert order: %w", err)
	}

	for _, s := range snapshots {
		_, err := tx.Exec(
			ctx,
			`
			INSERT INTO order_items (
				order_id,
				menu_item_id,
				name_snapshot,
				price_cents_snapshot,
				quantity,
				total_cents
			)
			VALUES ($1, $2, $3, $4, $5, $6)
			`,
			orderID,
			s.id,
			s.name,
			s.price,
			s.quantity,
			s.total,
		)
		if err != nil {
			return Draft{}, fmt.Errorf("insert order item: %w", err)
		}

		_, err = tx.Exec(
			ctx,
			`
			UPDATE menu_items
			SET stock_quantity = stock_quantity - $2,
			    updated_at = now()
			WHERE id = $1
			  AND stock_quantity IS NOT NULL
			`,
			s.id,
			s.quantity,
		)
		if err != nil {
			return Draft{}, fmt.Errorf("decrement stock: %w", err)
		}
	}

	if err := insertHistoryTx(ctx, tx, orderID, "pending_confirmation", "system", "order draft created"); err != nil {
		return Draft{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Draft{}, fmt.Errorf("commit order draft: %w", err)
	}

	return Draft{
		OrderID:               orderID,
		RestaurantID:          p.RestaurantID,
		ConfirmationURL:       confirmationURL,
		ConfirmationTimeoutMS: confirmationTimeoutMS,
		WebhookSecret:         webhookSecret,
		ExpiresAt:             expiresAt,
	}, nil
}

func (s *Store) GetOrderByID(ctx context.Context, id uuid.UUID) (domain.Order, error) {
	row := s.pool.QueryRow(
		ctx,
		`
		SELECT `+orderColumns+`
		FROM orders
		WHERE id = $1
		`,
		id,
	)

	o, err := scanOrder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, apperr.NotFound("order not found")
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("get order: %w", err)
	}

	rows, err := s.pool.Query(
		ctx,
		`
		SELECT menu_item_id, name_snapshot, price_cents_snapshot, quantity, total_cents
		FROM order_items
		WHERE order_id = $1
		`,
		id,
	)
	if err != nil {
		return domain.Order{}, fmt.Errorf("query order items: %w", err)
	}
	defer rows.Close()

	items := make([]domain.OrderItem, 0)
	for rows.Next() {
		var oi domain.OrderItem
		if err := rows.Scan(&oi.MenuItemID, &oi.Name, &oi.PriceCents, &oi.Quantity, &oi.TotalCents); err != nil {
			return domain.Order{}, fmt.Errorf("scan order item: %w", err)
		}
		items = append(items, oi)
	}
	if err := rows.Err(); err != nil {
		return domain.Order{}, fmt.Errorf("order item rows: %w", err)
	}

	o.Items = items
	return o, nil
}

func (s *Store) MarkOrderAwaitingPayment(
	ctx context.Context,
	orderID uuid.UUID,
	paymentToken uuid.UUID,
	paymentURL string,
	externalRef string,
	comment string,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin awaiting payment tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(
		ctx,
		`
		UPDATE orders SET
			status = 'awaiting_payment',
			payment_token = $2,
			payment_url = $3,
			expires_at = now() + interval '10 minutes',
			restaurant_confirmed_at = now(),
			restaurant_external_ref = $4,
			reject_reason = NULL,
			updated_at = now()
		WHERE id = $1
		  AND status = 'pending_confirmation'
		`,
		orderID,
		paymentToken,
		paymentURL,
		nullString(externalRef),
	)
	if err != nil {
		return fmt.Errorf("update order awaiting payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.Conflict("order cannot be moved to awaiting payment")
	}

	if err := insertHistoryTx(ctx, tx, orderID, "awaiting_payment", "restaurant", comment); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Store) RejectOrder(ctx context.Context, orderID uuid.UUID, status string, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reject order tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	err = tx.QueryRow(
		ctx,
		`SELECT status FROM orders WHERE id = $1 FOR UPDATE`,
		orderID,
	).Scan(&currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("order not found")
	}
	if err != nil {
		return fmt.Errorf("lock order for reject: %w", err)
	}

	if currentStatus != "pending_confirmation" {
		return nil
	}

	_, err = tx.Exec(
		ctx,
		`
		UPDATE orders SET
			status = $2,
			reject_reason = $3,
			expires_at = NULL,
			updated_at = now()
		WHERE id = $1
		`,
		orderID,
		status,
		nullString(reason),
	)
	if err != nil {
		return fmt.Errorf("reject order: %w", err)
	}

	if err := restockTx(ctx, tx, orderID); err != nil {
		return err
	}

	if err := insertHistoryTx(ctx, tx, orderID, status, "system", reason); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Store) PayOrderByToken(ctx context.Context, token uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin pay tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var orderID uuid.UUID
	var restaurantID uuid.UUID
	var status string
	var expiresAt *time.Time

	err = tx.QueryRow(
		ctx,
		`
		SELECT id, restaurant_id, status, expires_at
		FROM orders
		WHERE payment_token = $1
		FOR UPDATE
		`,
		token,
	).Scan(&orderID, &restaurantID, &status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("payment token not found")
	}
	if err != nil {
		return fmt.Errorf("lock order by payment token: %w", err)
	}

	if status != "awaiting_payment" {
		return apperr.Conflict("order cannot be paid in current status")
	}

	if expiresAt != nil && expiresAt.Before(time.Now()) {
		_, err = tx.Exec(
			ctx,
			`
			UPDATE orders SET
				status = 'expired',
				expires_at = NULL,
				reject_reason = 'payment expired',
				updated_at = now()
			WHERE id = $1
			`,
			orderID,
		)
		if err != nil {
			return fmt.Errorf("expire order: %w", err)
		}

		if err := restockTx(ctx, tx, orderID); err != nil {
			return err
		}

		if err := insertHistoryTx(ctx, tx, orderID, "expired", "system", "payment expired"); err != nil {
			return err
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit expired order: %w", err)
		}

		return apperr.New(http.StatusConflict, "ORDER_EXPIRED", "order expired before payment")
	}

	_, err = tx.Exec(
		ctx,
		`
		UPDATE orders SET
			status = 'paid',
			paid_at = now(),
			expires_at = NULL,
			updated_at = now()
		WHERE id = $1
		`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("mark order paid: %w", err)
	}

	if err := insertHistoryTx(ctx, tx, orderID, "paid", "system", "payment completed"); err != nil {
		return err
	}

	payload := map[string]any{
		"order": map[string]any{
			"id":            orderID,
			"restaurant_id": restaurantID,
			"status":        "paid",
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbox payload: %w", err)
	}

	_, err = tx.Exec(
		ctx,
		`
		INSERT INTO outbox_events (
			restaurant_id,
			event_type,
			aggregate_type,
			aggregate_id,
			payload,
			status
		)
		VALUES ($1, 'order.paid', 'order', $2, $3, 'pending')
		`,
		restaurantID,
		orderID,
		string(payloadBytes),
	)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *Store) CancelOrderByUser(ctx context.Context, orderID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin cancel tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var status string
	err = tx.QueryRow(
		ctx,
		`SELECT status FROM orders WHERE id = $1 FOR UPDATE`,
		orderID,
	).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("order not found")
	}
	if err != nil {
		return fmt.Errorf("lock order for cancel: %w", err)
	}

	if status != "awaiting_payment" {
		return apperr.Conflict("order can be cancelled only before payment")
	}

	_, err = tx.Exec(
		ctx,
		`
		UPDATE orders SET
			status = 'cancelled',
			cancelled_by = 'user',
			cancel_reason = 'user cancelled',
			expires_at = NULL,
			updated_at = now()
		WHERE id = $1
		`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}

	if err := restockTx(ctx, tx, orderID); err != nil {
		return err
	}

	if err := insertHistoryTx(ctx, tx, orderID, "cancelled", "user", "user cancelled"); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Store) RestaurantChangeStatus(
	ctx context.Context,
	restaurantID uuid.UUID,
	orderID uuid.UUID,
	newStatus string,
	allowedFrom []string,
	reason string,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin restaurant status tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus string
	err = tx.QueryRow(
		ctx,
		`
		SELECT status
		FROM orders
		WHERE id = $1 AND restaurant_id = $2
		FOR UPDATE
		`,
		orderID,
		restaurantID,
	).Scan(&currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("order not found")
	}
	if err != nil {
		return fmt.Errorf("lock order for restaurant status: %w", err)
	}

	if !contains(allowedFrom, currentStatus) {
		return apperr.Conflict("invalid status transition")
	}

	if newStatus == "cancelled" {
		_, err = tx.Exec(
			ctx,
			`
			UPDATE orders SET
				status = 'cancelled',
				cancelled_by = 'restaurant',
				cancel_reason = $2,
				updated_at = now()
			WHERE id = $1
			`,
			orderID,
			nullString(reason),
		)
		if err != nil {
			return fmt.Errorf("cancel order by restaurant: %w", err)
		}

		if err := restockTx(ctx, tx, orderID); err != nil {
			return err
		}
	} else {
		_, err = tx.Exec(
			ctx,
			`
			UPDATE orders SET
				status = $2,
				updated_at = now()
			WHERE id = $1
			`,
			orderID,
			newStatus,
		)
		if err != nil {
			return fmt.Errorf("update order status: %w", err)
		}
	}

	if err := insertHistoryTx(ctx, tx, orderID, newStatus, "restaurant", reason); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Store) ListRestaurantOrders(ctx context.Context, restaurantID uuid.UUID, status string) ([]domain.Order, error) {
	query := `SELECT ` + orderColumns + ` FROM orders WHERE restaurant_id = $1`
	args := []any{restaurantID}

	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	} else {
		query += ` AND status NOT IN ('pending_confirmation', 'awaiting_payment', 'rejected_by_restaurant', 'expired')`
	}

	query += ` ORDER BY created_at DESC LIMIT 100`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list restaurant orders: %w", err)
	}
	defer rows.Close()

	orders := make([]domain.Order, 0)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan restaurant order: %w", err)
		}
		orders = append(orders, o)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("restaurant order rows: %w", err)
	}

	return orders, nil
}

func (s *Store) ProcessOutbox(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin outbox tx: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(
		ctx,
		`
		WITH locked AS (
			SELECT id
			FROM outbox_events
			WHERE status = 'pending'
			  AND attempts < 5
			ORDER BY created_at
			LIMIT 5
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox_events oe
		SET status = 'processing',
		    attempts = oe.attempts + 1
		FROM locked
		WHERE oe.id = locked.id
		RETURNING oe.id, oe.restaurant_id, oe.event_type, oe.payload
		`,
	)
	if err != nil {
		return fmt.Errorf("select outbox events: %w", err)
	}

	events := make([]domain.OutboxEvent, 0)
	for rows.Next() {
		var e domain.OutboxEvent
		if err := rows.Scan(&e.ID, &e.RestaurantID, &e.EventType, &e.Payload); err != nil {
			rows.Close()
			return fmt.Errorf("scan outbox event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("outbox rows: %w", err)
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit outbox processing: %w", err)
	}

	for _, event := range events {
		if err := s.sendOutboxEvent(ctx, event); err != nil {
			_ = s.MarkOutboxFailed(ctx, event.ID, err.Error())
			continue
		}
		_ = s.MarkOutboxSent(ctx, event.ID)
	}

	return nil
}

func (s *Store) sendOutboxEvent(ctx context.Context, event domain.OutboxEvent) error {
	var webhookURL *string
	var webhookSecret *string

	err := s.pool.QueryRow(
		ctx,
		`SELECT webhook_url, webhook_secret FROM restaurants WHERE id = $1`,
		event.RestaurantID,
	).Scan(&webhookURL, &webhookSecret)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("restaurant not found")
	}
	if err != nil {
		return fmt.Errorf("get restaurant webhook: %w", err)
	}

	if webhookURL == nil || *webhookURL == "" {
		return fmt.Errorf("restaurant webhook url is empty")
	}

	envelope := map[string]any{
		"event_id":    uuid.New(),
		"event_type":  event.EventType,
		"occurred_at": time.Now().UTC(),
		"data":        json.RawMessage(event.Payload),
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal webhook envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Avito-Kitchen-Event-Type", event.EventType)

	if webhookSecret != nil && *webhookSecret != "" {
		req.Header.Set("X-Avito-Kitchen-Signature", hmacsign.Sign(*webhookSecret, body))
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	return fmt.Errorf("webhook returned status %d", resp.StatusCode)
}

func (s *Store) MarkOutboxSent(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(
		ctx,
		`
		UPDATE outbox_events
		SET status = 'sent',
		    processed_at = now(),
		    last_error = NULL
		WHERE id = $1
		`,
		id,
	)
	return err
}

func (s *Store) MarkOutboxFailed(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := s.pool.Exec(
		ctx,
		`
		UPDATE outbox_events
		SET status = CASE WHEN attempts < 5 THEN 'pending' ELSE 'failed' END,
		    last_error = $2
		WHERE id = $1
		`,
		id,
		nullString(reason),
	)
	return err
}

func (s *Store) ExpireOrders(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin expire tx: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(
		ctx,
		`
		SELECT id
		FROM orders
		WHERE status IN ('pending_confirmation', 'awaiting_payment')
		  AND expires_at IS NOT NULL
		  AND expires_at < now()
		ORDER BY expires_at
		LIMIT 5
		FOR UPDATE SKIP LOCKED
		`,
	)
	if err != nil {
		return fmt.Errorf("select expired orders: %w", err)
	}

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan expired order id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("expired order rows: %w", err)
	}
	rows.Close()

	for _, id := range ids {
		tag, err := tx.Exec(
			ctx,
			`
			UPDATE orders SET
				status = 'expired',
				expires_at = NULL,
				reject_reason = 'expired',
				updated_at = now()
			WHERE id = $1
			  AND status IN ('pending_confirmation', 'awaiting_payment')
			`,
			id,
		)
		if err != nil {
			return fmt.Errorf("expire order: %w", err)
		}

		if tag.RowsAffected() == 0 {
			continue
		}

		if err := restockTx(ctx, tx, id); err != nil {
			return err
		}

		if err := insertHistoryTx(ctx, tx, id, "expired", "system", "order expired"); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func restockTx(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) error {
	_, err := tx.Exec(
		ctx,
		`
		UPDATE menu_items mi
		SET stock_quantity = mi.stock_quantity + oi.quantity,
		    updated_at = now()
		FROM order_items oi
		WHERE oi.order_id = $1
		  AND mi.id = oi.menu_item_id
		  AND mi.stock_quantity IS NOT NULL
		`,
		orderID,
	)
	if err != nil {
		return fmt.Errorf("restock order items: %w", err)
	}
	return nil
}

func insertHistoryTx(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, status string, changedBy string, reason string) error {
	_, err := tx.Exec(
		ctx,
		`
		INSERT INTO order_status_history (order_id, status, changed_by, reason)
		VALUES ($1, $2, $3, $4)
		`,
		orderID,
		status,
		changedBy,
		nullString(reason),
	)
	if err != nil {
		return fmt.Errorf("insert status history: %w", err)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

type CreateRestaurantParams struct {
	ExternalID       string
	Name             string
	Description      string
	Category         string
	Address          string
	Phone            string
	DeliveryFeeCents int
	MinOrderCents    int
	ConfirmationURL  string
	WebhookURL       string
	WebhookSecret    string
	IsActive         bool
	IsOpen           bool
}

func (s *Store) CreateRestaurant(ctx context.Context, p CreateRestaurantParams) (domain.Restaurant, error) {
	row := s.pool.QueryRow(
		ctx,
		`
		INSERT INTO restaurants (
			external_id,
			name,
			description,
			category,
			address,
			phone,
			delivery_fee_cents,
			min_order_cents,
			is_active,
			is_open,
			confirmation_url,
			webhook_url,
			webhook_secret
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+restaurantColumns+`
		`,
		nullString(p.ExternalID),
		p.Name,
		nullString(p.Description),
		nullString(p.Category),
		nullString(p.Address),
		nullString(p.Phone),
		p.DeliveryFeeCents,
		p.MinOrderCents,
		p.IsActive,
		p.IsOpen,
		nullString(p.ConfirmationURL),
		nullString(p.WebhookURL),
		nullString(p.WebhookSecret),
	)

	r, err := scanRestaurant(row)
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("create restaurant: %w", err)
	}

	return r, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, restaurantID uuid.UUID, name string) (uuid.UUID, string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return uuid.Nil, "", fmt.Errorf("generate api key: %w", err)
	}

	plaintext := "rk_" + hex.EncodeToString(raw)
	keyHash := hashAPIKey(plaintext)

	prefix := plaintext
	if len(prefix) > 10 {
		prefix = prefix[:10]
	}

	var keyID uuid.UUID
	err := s.pool.QueryRow(
		ctx,
		`
		INSERT INTO restaurant_api_keys (restaurant_id, name, key_hash, key_prefix, is_active)
		VALUES ($1, $2, $3, $4, true)
		RETURNING id
		`,
		restaurantID,
		nullString(name),
		keyHash,
		prefix,
	).Scan(&keyID)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert api key: %w", err)
	}

	return keyID, plaintext, nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, keyID uuid.UUID) error {
	tag, err := s.pool.Exec(
		ctx,
		`UPDATE restaurant_api_keys SET is_active = false WHERE id = $1`,
		keyID,
	)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("api key not found")
	}
	return nil
}

func (s *Store) AdminListRestaurants(ctx context.Context) ([]domain.Restaurant, error) {
	rows, err := s.pool.Query(
		ctx,
		`SELECT `+restaurantColumns+` FROM restaurants ORDER BY created_at DESC LIMIT 200`,
	)
	if err != nil {
		return nil, fmt.Errorf("admin list restaurants: %w", err)
	}
	defer rows.Close()

	items := make([]domain.Restaurant, 0)
	for rows.Next() {
		r, err := scanRestaurant(rows)
		if err != nil {
			return nil, fmt.Errorf("scan restaurant: %w", err)
		}
		items = append(items, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("restaurant rows: %w", err)
	}

	return items, nil
}

type AdminUpdateRestaurantParams struct {
	IsActive *bool
	IsOpen   *bool
}

func (s *Store) AdminUpdateRestaurant(ctx context.Context, id uuid.UUID, p AdminUpdateRestaurantParams) (domain.Restaurant, error) {
	row := s.pool.QueryRow(
		ctx,
		`
		UPDATE restaurants SET
			is_active = COALESCE($2, is_active),
			is_open = COALESCE($3, is_open),
			updated_at = now()
		WHERE id = $1
		RETURNING `+restaurantColumns+`
		`,
		id,
		p.IsActive,
		p.IsOpen,
	)

	r, err := scanRestaurant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Restaurant{}, apperr.NotFound("restaurant not found")
	}
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("admin update restaurant: %w", err)
	}

	return r, nil
}
