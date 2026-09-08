CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE restaurants (
                             id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                             external_id text UNIQUE,
                             name text NOT NULL,
                             description text,
                             category text,
                             address text,
                             phone text,
                             delivery_fee_cents integer NOT NULL DEFAULT 0 CHECK (delivery_fee_cents >= 0),
                             min_order_cents integer NOT NULL DEFAULT 0 CHECK (min_order_cents >= 0),
                             estimated_delivery_minutes integer,
                             is_active boolean NOT NULL DEFAULT false,
                             is_open boolean NOT NULL DEFAULT false,
                             confirmation_url text,
                             confirmation_timeout_ms integer NOT NULL DEFAULT 5000,
                             webhook_url text,
                             webhook_secret text,
                             created_at timestamptz NOT NULL DEFAULT now(),
                             updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE restaurant_api_keys (
                                     id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                     restaurant_id uuid NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
                                     name text,
                                     key_hash text NOT NULL UNIQUE,
                                     key_prefix text NOT NULL,
                                     is_active boolean NOT NULL DEFAULT true,
                                     created_at timestamptz NOT NULL DEFAULT now(),
                                     last_used_at timestamptz
);

CREATE TABLE customers (
                           id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                           external_user_id text UNIQUE,
                           phone text,
                           full_name text,
                           created_at timestamptz NOT NULL DEFAULT now(),
                           updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE menu_categories (
                                 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                 restaurant_id uuid NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
                                 external_id text,
                                 name text NOT NULL,
                                 sort_order integer NOT NULL DEFAULT 0,
                                 is_active boolean NOT NULL DEFAULT true,
                                 created_at timestamptz NOT NULL DEFAULT now(),
                                 updated_at timestamptz NOT NULL DEFAULT now(),
                                 UNIQUE (restaurant_id, external_id)
);

CREATE TABLE menu_items (
                            id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                            restaurant_id uuid NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
                            category_id uuid REFERENCES menu_categories(id) ON DELETE SET NULL,
                            external_id text,
                            name text NOT NULL,
                            description text,
                            price_cents integer NOT NULL CHECK (price_cents >= 0),
                            image_url text,
                            is_available boolean NOT NULL DEFAULT true,
                            stock_quantity integer CHECK (stock_quantity >= 0),
                            sort_order integer NOT NULL DEFAULT 0,
                            created_at timestamptz NOT NULL DEFAULT now(),
                            updated_at timestamptz NOT NULL DEFAULT now(),
                            UNIQUE (restaurant_id, external_id)
);

CREATE TABLE orders (
                        id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                        restaurant_id uuid NOT NULL REFERENCES restaurants(id),
                        customer_id uuid NOT NULL REFERENCES customers(id),
                        status text NOT NULL CHECK (status IN (
                                                               'pending_confirmation',
                                                               'awaiting_payment',
                                                               'paid',
                                                               'preparing',
                                                               'delivering',
                                                               'completed',
                                                               'cancelled',
                                                               'rejected_by_restaurant',
                                                               'expired'
                            )),
                        subtotal_cents integer NOT NULL CHECK (subtotal_cents >= 0),
                        delivery_fee_cents integer NOT NULL CHECK (delivery_fee_cents >= 0),
                        total_cents integer NOT NULL CHECK (total_cents >= 0),
                        payment_method text NOT NULL,
                        payment_token uuid UNIQUE,
                        payment_url text,
                        expires_at timestamptz,
                        restaurant_confirmed_at timestamptz,
                        restaurant_external_ref text,
                        paid_at timestamptz,
                        customer_name text NOT NULL,
                        customer_phone text NOT NULL,
                        customer_address text NOT NULL,
                        customer_comment text,
                        idempotency_key text UNIQUE,
                        cancelled_by text,
                        cancel_reason text,
                        reject_reason text,
                        created_at timestamptz NOT NULL DEFAULT now(),
                        updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE order_items (
                             id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                             order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
                             menu_item_id uuid NOT NULL REFERENCES menu_items(id) ON DELETE RESTRICT,
                             name_snapshot text NOT NULL,
                             price_cents_snapshot integer NOT NULL CHECK (price_cents_snapshot >= 0),
                             quantity integer NOT NULL CHECK (quantity > 0),
                             total_cents integer NOT NULL CHECK (total_cents >= 0),
                             created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE order_status_history (
                                      id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                                      order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
                                      status text NOT NULL,
                                      changed_by text NOT NULL,
                                      reason text,
                                      created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE outbox_events (
                               id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
                               restaurant_id uuid REFERENCES restaurants(id) ON DELETE CASCADE,
                               event_type text NOT NULL,
                               aggregate_type text NOT NULL,
                               aggregate_id uuid NOT NULL,
                               payload jsonb NOT NULL,
                               status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'sent', 'failed')),
                               attempts integer NOT NULL DEFAULT 0,
                               last_error text,
                               created_at timestamptz NOT NULL DEFAULT now(),
                               processed_at timestamptz
);