FROM golang:1.23 AS build

WORKDIR /src

COPY . .

RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/main ./cmd/main
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/restaurant-demo ./cmd/restaurant-demo

FROM alpine:3.20 AS api-image

RUN apk add --no-cache ca-certificates
COPY --from=build /out/main /app/main
CMD ["/app/main"]

FROM alpine:3.20 AS demo-image

RUN apk add --no-cache ca-certificates
COPY --from=build /out/restaurant-demo /app/restaurant-demo
CMD ["/app/restaurant-demo"]