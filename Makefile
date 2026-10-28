.PHONY: up down admin test-contracts test-auth test-web e2e db-up

TEST_DATABASE_URL ?= postgres://tutor:tutor@localhost:5433/tutor
export TEST_DATABASE_URL

# Всё окружение: PostgreSQL, Mailpit, S3, Go-сервер с собранным React.
up:
	docker compose up -d --build --wait

down:
	docker compose down

# Выдать флаг преподавателя существующему аккаунту: make admin EMAIL=…
admin:
	@test -n "$(EMAIL)" || (echo "usage: make admin EMAIL=…" && exit 2)
	docker compose exec backend /server admin "$(EMAIL)"

db-up:
	docker compose up -d --wait postgres

# Контракты: OpenAPI, схемы курса и результатов агентов на валидных и невалидных примерах;
# типы React из OpenAPI.
test-contracts:
	go test ./contracts/ ./backend/internal/course/
	cd web && npm run gen:api && npm run typecheck

# Вход по коду, сессии, роли и изоляция учеников (реальный PostgreSQL, ответы сверяются с OpenAPI).
test-auth: db-up
	go test ./backend/internal/httpapi/ -run 'Code|Session|Teacher|Learner|Logout'

# Браузерные сценарии против запущенного make up.
e2e:
	cd web && npx playwright test
