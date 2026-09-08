package worker

import (
	"context"
	"time"

	"avitotask/internal/applog"
	"avitotask/internal/store"
)

func Outbox(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	applog.Info("outbox worker started")

	for {
		select {
		case <-ctx.Done():
			applog.Info("outbox worker stopped")
			return
		case <-ticker.C:
			if err := st.ProcessOutbox(ctx); err != nil {
				applog.Error("outbox worker iteration failed", "error", err.Error())
			}
		}
	}
}

func Expirer(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	applog.Info("expirer worker started")

	for {
		select {
		case <-ctx.Done():
			applog.Info("expirer worker stopped")
			return
		case <-ticker.C:
			if err := st.ExpireOrders(ctx); err != nil {
				applog.Error("expirer worker iteration failed", "error", err.Error())
			}
		}
	}
}
