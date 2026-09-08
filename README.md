# Avito.Kitchen (MVP)

Сервис заказа еды из заведений-партнёров. MVP состоит из:

- **Core API** (`cmd/main`) — публичное клиентское API, закрытое API заведений, админское API, мок-оплата;
- **Restaurant Demo** (`cmd/restaurant-demo`) — пример интеграции заведения;
- **PostgreSQL** — основное хранилище.

Стек: Go 1.23, стандартный `net/http`, PostgreSQL, `pgx` (SQL вручную), `golang-migrate`, Docker Compose.

## Быстрый старт

```bash
docker compose up --build
```

- Веб-интерфейс: http://localhost:8080
- Демо-ресторан: http://localhost:8081
- OpenAPI: `openapi/api.yaml`

Пример флоу:

```bash
# 1. список заведений
curl http://localhost:8080/api/v1/restaurants

# 2. меню
curl http://localhost:8080/api/v1/restaurants/{restaurantId}/menu

# 3. создание заказа (резерв + подтверждение у ресторана)
curl -X POST http://localhost:8080/api/v1/orders \
  -H 'Content-Type: application/json' \
  -d '{"restaurant_id":"...","customer":{"name":"Иван","phone":"+79990000000","address":"..."},"items":[{"menu_item_id":"...","quantity":1}]}'

# 4. оплата (мок)
curl -X POST http://localhost:8080/mock/pay/{paymentToken}/pay
```

Админское API:

```bash
curl -X POST http://localhost:8080/api/v1/admin/restaurants \
  -H 'X-Admin-Token: admin-token' \
  -H 'Content-Type: application/json' \
  -d '{"name":"Новое кафе","confirmation_url":"https://.../confirm","webhook_url":"https://.../webhook"}'
```

## Архитектура (C4, Level 2)

![architecture.png](docs/images/architecture.png)

## Архитектура (C4, Level 3, Core API)

![architecture2.png](docs/images/architecture2.png)

## CJM пользователя

![usercjm.png](docs/images/usercjm.png)

| Шаг | Боль пользователя | Решение в MVP |
|---|---|---|
| Выбор заведения | Не понятно, что открыто | Фильтр `open_now`, только активные |
| Меню | Товар может закончиться | Поле `stock_quantity`, проверка при заказе |
| Оплата | Деньги спишут, а ресторан не сможет приготовить | Подтверждение у ресторана ДО оплаты |
| Ожидание | Непонятен статус | Статусы `paid → preparing → delivering → completed` |
| Отмена | Хочу отменить, пока не оплатил | `POST /orders/{id}/cancel` в `awaiting_payment` |

## CJM заведения

![restaurantcjm.png](docs/images/restaurantcjm.png)


| Шаг | Боль заведения | Решение в MVP |
|---|---|---|
| Подключение | Нужен закрытый доступ | Админ создаёт заведение и выдаёт API-ключ |
| Меню | Дублирование ввода | `menu/sync` с upsert по `external_id` |
| Заказы | Можно пропустить заказ | Webhook + список заказов через API |
| Оверселлинг | Продать то, чего нет | Резерв товара в транзакции |

## Жизненный цикл заказа

![orderlifecicle.png](docs/images/orderlifecicle.png)


Предварительное подтверждение ресторана считается согласием выполнить заказ: после оплаты отдельный `accept` не нужен.

## Схема БД (ERD)

![dbscheme.png](docs/images/dbscheme.png)


### Описание таблиц

| Таблица | Назначение |
|---|---|
| `restaurants` | Заведения: профиль, доставка, минимум заказа, URL подтверждения и вебхука |
| `restaurant_api_keys` | Хэши API-ключей заведений (plaintext показывается один раз) |
| `customers` | Покупатели: привязка к `external_user_id` клиента |
| `menu_categories`, `menu_items` | Меню заведения; `stock_quantity = NULL` — бесконечный остаток |
| `orders` | Заказы всех статусов, включая pre-payment; `payment_token` для мок-оплаты |
| `order_items` | Снапшот позиций заказа (имя и цена на момент заказа) |
| `order_status_history` | История переходов статусов |
| `outbox_events` | Транзакционный outbox для вебхуков заведению |

Миграции: `migrations/*.sql`, применяются через `golang-migrate` (сервис `migrations` в compose или `make migrate-up`).

## Интеграция с заведением

### Запрос подтверждения (Core → заведение)

```http
POST {confirmation_url}
X-Avito-Kitchen-Signature: sha256=<hmac sha256 тела>
```

```json
{"order_id":"...","restaurant_id":"...","expires_at":"...","items":[{"menu_item_id":"...","quantity":1}]}
```

Ответ заведения:

```json
{"confirmed": true, "external_order_ref": "...", "comment": "..."}
```

### Вебхуки (Core → заведение)

```json
{"event_id":"...","event_type":"order.paid","occurred_at":"...","data":{"order":{"id":"...","status":"paid"}}}
```

События доставляются из outbox с ретраями (до 5 попыток).

## Тесты

Интеграционные тесты в `tests/integration_test.go` покрывают:

| Тест | Сценарий |
|---|---|
| `TestLocalStockRejection` | локальный отказ по остаткам |
| `TestRestaurantRejection` | отказ ресторана до оплаты |
| `TestPaymentFlow` | успешная оплата и обработка заказа |
| `TestPaymentExpiration` | просрочка оплаты |
| `TestRestaurantStatusTransitions` | изменение статусов рестораном, запрещённые переходы, отмена |
| `TestAdminAPI` | создание заведений и ключей, отзыв ключей |

Запуск:

```bash
docker compose up -d postgres
psql "postgres://app:app@localhost:5432/postgres" -c "CREATE DATABASE avito_kitchen_test;"
TEST_DB_DSN="postgres://app:app@localhost:5432/avito_kitchen_test?sslmode=disable" go test ./tests/ -v
```

## Качество

- Линтер: `golangci-lint` (`.golangci.yml`), запуск `make lint`.
- Миграции: `make migrate-up`, `make migrate-down`.
- SQL только вручную, без ORM.

## Упрощения MVP

1. Нет реальной авторизации пользователей (идентификация упрощённая).
2. Оплата мокнутая (`/mock/pay/...`), реальный PSP не подключён.
3. Подтверждение ресторана синхронное с таймаутом.
4. Нет возвратов денег при отмене оплаченного заказа (только возврат остатков).
5. Нет гео-зон, расписания работы, рейтингов, промокодов.
6. Админская авторизация — статический токен `X-Admin-Token`.