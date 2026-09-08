package config

import (
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr   string
	DBDSN      string
	BaseURL    string
	AdminToken string
	LogLevel   string
	LogFormat  string

	SeedDemo bool

	DemoExternalID       string
	DemoRestaurantName   string
	DemoAPIKey           string
	DemoWebhookSecret    string
	DemoConfirmationURL  string
	DemoWebhookURL       string
	DemoDeliveryFeeCents int
	DemoMinOrderCents    int
}

func Load() Config {
	return Config{
		HTTPAddr:             getEnv("HTTP_ADDR", ":8080"),
		DBDSN:                getEnv("DB_DSN", "postgres://app:app@localhost:5432/avito_kitchen?sslmode=disable"),
		BaseURL:              getEnv("BASE_URL", "http://localhost:8080"),
		AdminToken:           getEnv("ADMIN_TOKEN", "admin-token"),
		LogLevel:             getEnv("LOG_LEVEL", "info"),
		LogFormat:            getEnv("LOG_FORMAT", "json"),
		SeedDemo:             getBool("SEED_DEMO", true),
		DemoExternalID:       getEnv("DEMO_EXTERNAL_ID", "demo-pizza"),
		DemoRestaurantName:   getEnv("DEMO_RESTAURANT_NAME", "Пицца Мама"),
		DemoAPIKey:           getEnv("DEMO_API_KEY", "demo-key"),
		DemoWebhookSecret:    getEnv("DEMO_WEBHOOK_SECRET", "demo-secret"),
		DemoConfirmationURL:  getEnv("DEMO_CONFIRMATION_URL", "http://restaurant-demo:8081/confirm-order"),
		DemoWebhookURL:       getEnv("DEMO_WEBHOOK_URL", "http://restaurant-demo:8081/webhook/events"),
		DemoDeliveryFeeCents: getInt("DEMO_DELIVERY_FEE_CENTS", 19900),
		DemoMinOrderCents:    getInt("DEMO_MIN_ORDER_CENTS", 10000),
	}
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func getBool(key string, def bool) bool {
	v := getEnv(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getInt(key string, def int) int {
	v := getEnv(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
