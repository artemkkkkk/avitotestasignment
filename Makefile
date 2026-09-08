MIGRATE_DSN ?= postgres://app:app@localhost:5432/avito_kitchen?sslmode=disable

.PHONY: tidy lint fmt migrate-up migrate-down migrate-new run-api run-demo test

tidy:
	go mod tidy

fmt:
	gofmt -w .
	golangci-lint run --fix ./...

lint:
	golangci-lint run ./...

migrate-up:
	migrate -path migrations -database "$(MIGRATE_DSN)" up

migrate-down:
	migrate -path migrations -database "$(MIGRATE_DSN)" down 1

migrate-new:
	migrate create -ext sql -dir migrations $(NAME)

run-api:
	go run ./cmd/main

run-demo:
	go run ./cmd/restaurant-demo

test:
	go test ./... -v