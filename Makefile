.PHONY: up down admin test-contracts test-auth test-attendance e2e db-up

CODEX_IMAGE := tutor-codex:0.163.0
export AGENT_DATA_DIR := $(CURDIR)/.data/agents

TEST_DATABASE_URL ?= postgres://tutor:tutor@localhost:5433/tutor
export TEST_DATABASE_URL

# compose читает .env; в чистом checkout он создаётся из примера.
.env:
	cp .env.example .env

# Всё окружение: PostgreSQL, Mailpit, S3, Go-сервер с собранным React.
# AGENT_FAKE=1 make up — детерминированные агенты без вызовов модели (браузерные сценарии).
up: .env codex-image
	mkdir -p $(AGENT_DATA_DIR)
	docker compose up -d --build --wait

# Образ Codex app-server для сессий агентов.
codex-image:
	docker build -q -t $(CODEX_IMAGE) infra/codex

down:
	docker compose down

# Выдать флаг преподавателя существующему аккаунту: make admin EMAIL=…
admin:
	@test -n "$(EMAIL)" || (echo "usage: make admin EMAIL=…" && exit 2)
	docker compose exec backend /server admin "$(EMAIL)"

db-up: .env
	docker compose up -d --wait postgres

# Контракты: OpenAPI, схемы курса и результатов агентов на валидных и невалидных примерах;
# типы React из OpenAPI.
test-contracts:
	go test ./contracts/ ./backend/internal/course/
	cd web && npm run gen:api && npm run typecheck

# Вход по коду, сессии, роли и изоляция учеников (реальный PostgreSQL, ответы сверяются с OpenAPI).
test-auth: db-up
	go test ./backend/internal/httpapi/ -run 'Code|Session|Teacher|Learner|Logout'

# Посещения и показатели посещаемости (AC-11): формула на фиксированном времени и HTTP.
test-attendance: db-up
	go test ./backend/internal/visits/ ./backend/internal/httpapi/ -run 'Attendance|Visit|Window|Teacher'

# Интервью с Наставником-планировщиком на управляемых ответах модели.
test-planning: db-up
	go test ./backend/internal/agents/ ./backend/internal/httpapi/ -run 'Interview|Skipping|Agent|Output|Mentor|Restart|OutputSchema|ParseResult'

# Реальный вызов модели через контейнер Codex (нужен OPENAI_API_KEY в .env).
verify-codex-live: .env codex-image
	set -a && . ./.env && set +a && LIVE_CODEX=1 go test ./backend/internal/agents/codex/ -run Live -v -count=1 -timeout 10m

# Браузерные сценарии против запущенного make up.
e2e:
	cd web && npx playwright test
