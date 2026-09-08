package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"avitotask/internal/hmacsign"
)

func main() {
	log.Println("=== starting restaurant-demo ===")
	coreBaseURL := getEnv("CORE_BASE_URL", "http://localhost:8080")
	apiKey := getEnv("RESTAURANT_API_KEY", "demo-key")
	addr := getEnv("WEBHOOK_ADDR", ":8081")
	publicBaseURL := getEnv("PUBLIC_BASE_URL", "http://localhost:8081")
	secret := getEnv("WEBHOOK_SECRET", "demo-secret")
	autoConfirm := getBool("AUTO_CONFIRM", true)
	simulateReject := getBool("SIMULATE_REJECT", false)
	autoProcessPaid := getBool("AUTO_PROCESS_PAID_ORDERS", true)

	if err := waitCore(coreBaseURL); err != nil {
		log.Fatalf("core api is not available: %v", err)
	}

	if err := register(coreBaseURL, apiKey, publicBaseURL); err != nil {
		log.Fatalf("failed to register demo restaurant: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /confirm-order", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if secret != "" {
			signature := r.Header.Get("X-Avito-Kitchen-Signature")
			if signature == "" || !hmacsign.Verify(secret, body, signature) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		var req struct {
			OrderID string `json:"order_id"`
		}
		_ = json.Unmarshal(body, &req)

		confirmed := autoConfirm && !simulateReject

		resp := map[string]any{
			"confirmed": confirmed,
		}

		if confirmed {
			resp["external_order_ref"] = "demo-order-" + req.OrderID
			resp["comment"] = "order confirmed by demo restaurant"
		} else {
			resp["reason"] = "demo rejection"
			resp["comment"] = "demo restaurant rejected the order"
		}

		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /webhook/events", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if secret != "" {
			signature := r.Header.Get("X-Avito-Kitchen-Signature")
			if signature != "" && !hmacsign.Verify(secret, body, signature) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		var event struct {
			EventType string `json:"event_type"`
			Data      struct {
				Order struct {
					ID string `json:"id"`
				} `json:"order"`
			} `json:"data"`
		}

		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}

		if event.EventType == "order.paid" && autoProcessPaid && event.Data.Order.ID != "" {
			go processOrder(coreBaseURL, apiKey, event.Data.Order.ID)
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "accepted"})
	})

	log.Printf("restaurant demo started on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func waitCore(baseURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}

	for i := 0; i < 30; i++ {
		resp, err := client.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(time.Second)
	}

	return fmt.Errorf("core api health check failed")
}

func register(coreBaseURL, apiKey, publicBaseURL string) error {
	patchBody := map[string]any{
		"is_open":          true,
		"confirmation_url": publicBaseURL + "/confirm-order",
		"webhook_url":      publicBaseURL + "/webhook/events",
	}

	if err := callCore(coreBaseURL, apiKey, http.MethodPatch, "/api/v1/restaurant/me", patchBody); err != nil {
		return fmt.Errorf("patch restaurant profile: %w", err)
	}

	menu := map[string]any{
		"categories": []map[string]any{
			{
				"external_id": "pizza",
				"name":        "Пицца",
				"sort_order":  10,
				"items": []map[string]any{
					{
						"external_id":    "pizza-margarita",
						"name":           "Маргарита",
						"description":    "Классическая пицца",
						"price_cents":    59900,
						"is_available":   true,
						"stock_quantity": 10,
						"sort_order":     10,
					},
					{
						"external_id":    "pizza-pepperoni",
						"name":           "Пепперони",
						"description":    "Острая пицца",
						"price_cents":    69900,
						"is_available":   true,
						"stock_quantity": 0,
						"sort_order":     20,
					},
				},
			},
			{
				"external_id": "drinks",
				"name":        "Напитки",
				"sort_order":  20,
				"items": []map[string]any{
					{
						"external_id":    "cola",
						"name":           "Кола",
						"description":    "0.5 л",
						"price_cents":    15000,
						"is_available":   true,
						"stock_quantity": 100,
						"sort_order":     10,
					},
				},
			},
		},
	}

	if err := callCore(coreBaseURL, apiKey, http.MethodPost, "/api/v1/restaurant/menu/sync", menu); err != nil {
		return fmt.Errorf("sync menu: %w", err)
	}

	return nil
}

func processOrder(coreBaseURL, apiKey, orderID string) {
	steps := []string{"preparing", "delivering", "complete"}

	for _, step := range steps {
		time.Sleep(700 * time.Millisecond)
		path := fmt.Sprintf("/api/v1/restaurant/orders/%s/%s", orderID, step)
		if err := callCore(coreBaseURL, apiKey, http.MethodPost, path, nil); err != nil {
			log.Printf("failed to move order %s to %s: %v", orderID, step, err)
			return
		}
		log.Printf("order %s moved to %s", orderID, step)
	}
}

func callCore(coreBaseURL, apiKey, method, path string, body any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, coreBaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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
