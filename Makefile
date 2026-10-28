.PHONY: admin build codex-image db-up down e2e test-attendance test-auth test-contracts test-generation test-learning test-planning test-telemetry up verify-codex-live verify-generation-live verify-observability verify-planning-live 

CODEX_IMAGE := tutor-codex:0.163.0
export AGENT_DATA_DIR := $(CURDIR)/.data/agents

TEST_DATABASE_URL ?= postgres://tutor:tutor@localhost:5433/tutor
export TEST_DATABASE_URL

# compose читает .env; в чистом checkout он создаётся из примера.
.env:
	cp .env.example .env

# Всё окружение: PostgreSQL, Mailpit, S3, Go-сервер с собранным React.
# AGENT_FAKE=1 make up — детерминированные агенты без вызовов модели (браузерные сценарии).
up: .env codex-image build
	mkdir -p $(AGENT_DATA_DIR)
	docker compose up -d --build --wait

# Бинарник сервера для контейнера и собранный React (на хосте: нужны Go и Node).
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=$$(docker version -f '{{.Server.Arch}}') go build -o bin/server-linux ./backend/cmd/server
	cd web && (test -d node_modules || npm ci) && npm run build

# Образ Codex app-server для сессий агентов.
codex-image:
	docker image inspect $(CODEX_IMAGE) >/dev/null 2>&1 || docker build -q -t $(CODEX_IMAGE) infra/codex

down:
	docker compose down

# Выдать флаг преподавателя существующему аккаунту: make admin EMAIL=…
admin:
	@test -n "$(EMAIL)" || (echo "usage: make admin EMAIL=…" && exit 2)
	docker compose exec backend /server admin "$(EMAIL)"

db-up: .env
	docker compose up -d --wait postgres s3

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
	go test ./backend/internal/agents/ ./backend/internal/httpapi/ -run 'Interview|Skip|Agent|Output|Mentor|Restart|Plan|Review|Goal|Confirm|GitHub'

# Реальный вызов модели через контейнер Codex (нужен OPENAI_API_KEY в .env).
verify-codex-live: .env codex-image
	set -a && . ./.env && set +a && LIVE_CODEX=1 go test ./backend/internal/agents/codex/ -run Live -v -count=1 -timeout 10m

# Генерация, структурная проверка, ревью, загрузка в S3 и восстановление папки наставника.
test-generation: db-up
	go test ./backend/internal/course/ ./backend/internal/httpapi/ -run 'Generat|Published|Invalid|Structure|Practice|ContentReview|ReviewError|Approval|Upload|Mentor'

# Встроенные вопросы: оценка, подсказка и разбор, короткий ответ, поздний результат, изоляция.
test-learning: db-up
	go test ./backend/internal/httpapi/ -run 'Choice|Mistake|InvalidAnswers|ShortAnswer|ReviewerError|Attempts'

# Реальные планировщик и ревьюер плана на промптах agent-config/ (пропуск интервью → одобренный план).
verify-planning-live: .env codex-image db-up
	set -a && . ./.env && set +a && LIVE_CODEX=1 go test ./backend/internal/planning/ -run Live -v -count=1 -timeout 30m

# Метрики, логи и трейсы: детерминированная часть.
test-telemetry: db-up
	go test ./backend/internal/telemetry/

# AC-12/AC-13 на живом стеке: перезапускает бэкенд с тестовыми маршрутами 5xx/slow и
# несуществующей моделью планировщика (реальная ошибка внешнего вызова), сверяет
# Prometheus, Tempo, Loki и панели Grafana, затем возвращает обычную конфигурацию.
verify-observability: .env
	AGENT_FAKE=0 OBSERVABILITY_TEST_ROUTES=1 MODEL_PLANNER=nonexistent-model-for-observability-check $(MAKE) up
	set -a && . ./.env && set +a && python3 scripts/verify-observability.py; status=$$?; $(MAKE) up; exit $$status

# Реальная генерация курса PM: план → подтверждение → генерация → ревью → S3 (десятки минут, расход API).
verify-generation-live: .env codex-image db-up
	mkdir -p .data/live
	set -a && . ./.env && set +a && LIVE_CODEX=1 LIVE_OUT_DIR=$(CURDIR)/.data/live go test ./backend/internal/generation/ -run Live -v -count=1 -timeout 120m

# Браузерные сценарии против запущенного make up.
e2e:
	cd web && npx playwright test
