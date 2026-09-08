package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"avitotask/internal/applog"
	"avitotask/internal/config"
	"avitotask/internal/httpapi"
	"avitotask/internal/service"
	"avitotask/internal/store"
	"avitotask/internal/worker"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	cfg := config.Load()
	applog.Init(cfg.LogLevel, cfg.LogFormat)

	applog.Info("starting api",
		"http_addr", cfg.HTTPAddr,
		"base_url", cfg.BaseURL,
		"seed_demo", cfg.SeedDemo,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DBDSN)
	if err != nil {
		log.Fatalf("create pool: %v", err)
	}
	defer pool.Close()

	if err := waitDB(ctx, pool); err != nil {
		applog.Error("database is not available", "error", err.Error())
		os.Exit(1)
	}
	applog.Info("database connected")

	st := store.New(pool)

	if cfg.SeedDemo {
		err := st.SeedDemo(ctx, store.SeedDemoParams{
			ExternalID:       cfg.DemoExternalID,
			Name:             cfg.DemoRestaurantName,
			Description:      "Демо-заведение",
			Category:         "pizza",
			Address:          "Москва, ул. Пушкина, 1",
			Phone:            "+79990000000",
			APIKey:           cfg.DemoAPIKey,
			WebhookSecret:    cfg.DemoWebhookSecret,
			ConfirmationURL:  cfg.DemoConfirmationURL,
			WebhookURL:       cfg.DemoWebhookURL,
			DeliveryFeeCents: cfg.DemoDeliveryFeeCents,
			MinOrderCents:    cfg.DemoMinOrderCents,
		})
		if err != nil {
			applog.Error("seed demo failed", "error", err.Error())
			os.Exit(1)
		}
		applog.Info("demo restaurant seeded", "external_id", cfg.DemoExternalID)
	}

	orderService := service.NewOrderService(st, service.NewHTTPConfirmer(), cfg.BaseURL)

	server := httpapi.NewServer(st, orderService, cfg.BaseURL)
	server.AdminToken = cfg.AdminToken

	mux := server.Routes()

	go worker.Outbox(ctx, st)
	go worker.Expirer(ctx, st)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			applog.Error("http server error", "error", err.Error())
			os.Exit(1)
		}
	}()

	applog.Info("api started", "addr", cfg.HTTPAddr)

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = httpServer.Shutdown(shutdownCtx)
	applog.Info("api stopped")
}

func waitDB(ctx context.Context, pool *pgxpool.Pool) error {
	for i := 0; i < 30; i++ {
		if err := pool.Ping(ctx); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("database is not available")
}
