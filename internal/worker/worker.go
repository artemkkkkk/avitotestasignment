package worker

import (
	"context"
	"log"
	"time"

	"avitotask/internal/store"
)

func Outbox(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.ProcessOutbox(ctx); err != nil {
				log.Printf("outbox worker error: %v", err)
			}
		}
	}
}

func Expirer(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.ExpireOrders(ctx); err != nil {
				log.Printf("expirer worker error: %v", err)
			}
		}
	}
}
