.PHONY: test-contracts

# Контракты: OpenAPI, схемы курса и результатов агентов на валидных и невалидных примерах.
test-contracts:
	go test ./contracts/ ./backend/internal/course/
