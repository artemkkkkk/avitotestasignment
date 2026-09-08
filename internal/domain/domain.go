package domain

import (
	"time"

	"github.com/google/uuid"
)

type Restaurant struct {
	ID                       uuid.UUID
	ExternalID               *string
	Name                     string
	Description              *string
	Category                 *string
	Address                  *string
	Phone                    *string
	DeliveryFeeCents         int
	MinOrderCents            int
	EstimatedDeliveryMinutes *int
	IsActive                 bool
	IsOpen                   bool
	ConfirmationURL          *string
	ConfirmationTimeoutMS    int
	WebhookURL               *string
	WebhookSecret            *string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

type Category struct {
	ID           uuid.UUID
	RestaurantID uuid.UUID
	ExternalID   *string
	Name         string
	SortOrder    int
	IsActive     bool
}

type MenuItem struct {
	ID            uuid.UUID
	RestaurantID  uuid.UUID
	CategoryID    *uuid.UUID
	ExternalID    *string
	Name          string
	Description   *string
	PriceCents    int
	ImageURL      *string
	IsAvailable   bool
	StockQuantity *int
	SortOrder     int
}

type Customer struct {
	ID             uuid.UUID
	ExternalUserID *string
	Phone          *string
	FullName       *string
}

type OrderItem struct {
	MenuItemID uuid.UUID
	Name       string
	PriceCents int
	Quantity   int
	TotalCents int
}

type Order struct {
	ID                    uuid.UUID
	RestaurantID          uuid.UUID
	CustomerID            uuid.UUID
	Status                string
	SubtotalCents         int
	DeliveryFeeCents      int
	TotalCents            int
	PaymentMethod         string
	PaymentToken          *uuid.UUID
	PaymentURL            *string
	ExpiresAt             *time.Time
	RestaurantConfirmedAt *time.Time
	RestaurantExternalRef *string
	PaidAt                *time.Time
	CustomerName          string
	CustomerPhone         string
	CustomerAddress       string
	CustomerComment       *string
	CreatedAt             time.Time
	Items                 []OrderItem
}

type OutboxEvent struct {
	ID           uuid.UUID
	RestaurantID uuid.UUID
	EventType    string
	AggregateID  uuid.UUID
	Payload      []byte
}
