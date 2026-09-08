CREATE INDEX idx_restaurants_active_open ON restaurants (is_active, is_open);
CREATE INDEX idx_menu_categories_restaurant ON menu_categories (restaurant_id);
CREATE INDEX idx_menu_items_restaurant ON menu_items (restaurant_id);
CREATE INDEX idx_orders_restaurant_status_created ON orders (restaurant_id, status, created_at DESC);
CREATE INDEX idx_orders_pending_expires ON orders (expires_at)
    WHERE status IN ('pending_confirmation', 'awaiting_payment');
CREATE INDEX idx_outbox_pending ON outbox_events (status, created_at);