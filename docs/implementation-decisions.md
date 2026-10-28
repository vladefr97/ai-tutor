# Технические решения PoC

Статус: принято исполнителем 28 октября 2026 года (issue #10). Основание: [приёмка](poc-acceptance.md), [ADR-0002](adr/0002-otdelnyj-sajt-dlya-poc.md), [ADR-0003](adr/0003-codex-runtime.md), [план](superpowers/plans/2026-10-25-ai-tutor-poc.md). Версии проверены по первоисточникам на эту дату. Меняются здесь же, с причиной.

## Сводка компонентов

| Компонент | Выбор | Версия |
|---|---|---|
| Бэкенд | Go, stdlib `net/http` с шаблонами маршрутов Go 1.22+ | Go 1.27 |
| БД | PostgreSQL, драйвер `jackc/pgx/v5`, миграции `pressly/goose/v3` | образ `postgres:17-bookworm`; pgx v5.11.0; goose v3.29.0 |
| JSON Schema | `santhosh-tekuri/jsonschema/v6` (draft 2020-12) | v6.0.3 |
| OpenAPI | `getkin/kin-openapi`: валидация ответов хендлеров в Go-тестах | v0.152.0 |
| Фронт | React 19 + Vite + TypeScript, типы из OpenAPI через `openapi-typescript` | версии фиксируются в `web/package-lock.json` |
| Браузерные сценарии | Playwright (`@playwright/test`) | lock-файл |
| S3 | Versity Gateway, бэкенд `posix` | `versity/versitygw:v1.10.0` |
| Почта | Mailpit | `axllent/mailpit:v1.32.0` |
| Трейсы, логи, Grafana | `grafana/otel-lgtm` (OTel Collector + Tempo + Loki + Grafana в одном контейнере) | `grafana/otel-lgtm:0.38.1` |
| Метрики | Prometheus, scrape `/metrics` приложения (`prometheus/client_golang`) | `prom/prometheus:v3.15.1` |
| OTel SDK | `go.opentelemetry.io/otel`, экспорт OTLP в otel-lgtm; логи `log/slog` с `trace_id`/`span_id` | v1.47.0 |
| Агентный движок | Codex app-server в Docker | `@openai/codex@0.163.0` |
| GitHub | GitHub App в организации платформы + OAuth user-to-server | REST API `2022-11-28` |
| Вебхуки локально | smee.io + `smee-client` | lock-файл |

## Хранилище и данные

- **PostgreSQL 17.** Предпочтение пользователя (ADR-0002). Миграции goose в `backend/migrations/`, применяются сервером при старте. Том `pgdata` переживает перезапуск.
- **S3: Versity Gateway (posix).** MinIO CE прекратил публикацию образов в октябре 2025 и ушёл в режим поддержки, публичный репозиторий архивирован ([itsfoss](https://itsfoss.com/news/minio-moves-away-from-open-source/), [обзор альтернатив](https://pinggy.io/blog/minio_archived_self_hosted_s3_alternatives/)). Versity: Apache-2.0, один контейнер, учётные данные через `ROOT_ACCESS_KEY`/`ROOT_SECRET_KEY`, объекты лежат файлами в томе ([README](https://github.com/versity/versitygw)). Нам нужны только PutObject/GetObject/ListObjectsV2/DeleteObject. Garage требует инициализации layout и ключей отдельными командами, SeaweedFS тяжелее; для локального PoC это лишнее. Клиент из Go: `aws-sdk-go-v2/service/s3` с `UsePathStyle`.
- **Раскладка версии курса в S3:** `courses/{courseId}/versions/{versionId}/public/...` и `.../private/...`. Учебному наставнику скачивается только `public/`. Признак готовности версии — запись в БД после успешной загрузки всех объектов, а не наличие объектов.

## Почта и вход

- **Mailpit v1.32.0** ([docs](https://mailpit.axllent.org/docs/)): SMTP `1025`, веб и API `8025`. Тесты читают письма через REST: `GET /api/v1/search?query=to:<email>`, `GET /api/v1/message/{ID}`, очистка `DELETE /api/v1/messages`.
- **Код:** 6 цифр из `crypto/rand`, в БД только хеш (SHA-256 с серверным pepper), одноразовый, `AUTH_CODE_TTL` (по умолчанию 10 мин), не больше 5 неверных вводов на код.
- **Сессия:** непрозрачный токен 32 байта в cookie `HttpOnly; SameSite=Lax; Path=/` (`Secure` вне localhost), в БД хеш; `SESSION_TTL` (по умолчанию 30 дней). Identity берётся только из сессии.
- **Преподаватель:** при старте upsert аккаунта `ADMIN_EMAIL` с флагом `is_teacher`; `make admin EMAIL=…` выставляет флаг существующему аккаунту.

## Наблюдаемость

- **Минимум контейнеров:** `grafana/otel-lgtm:0.38.1` ([репозиторий](https://github.com/grafana/docker-otel-lgtm)) принимает OTLP (4317/4318), хранит трейсы в Tempo и логи в Loki, показывает Grafana (3000). Prometheus отдельным контейнером `prom/prometheus:v3.15.1`, чтобы сбор метрик был именно scrape-моделью из AC-12. Datasource Prometheus и панели провиженятся в Grafana из `infra/observability/`.
- **Логи:** `slog` JSON в stdout и OTLP-экспорт через мост `otelslog`; каждая запись в контексте запроса несёт `trace_id`. В Grafana настраивается переход Tempo → Loki по `trace_id`.
- **Метрики:** HTTP-метки только `method`, `route` (шаблон `r.Pattern`), `code`. ID учеников и курсов в labels не попадают. Внешние вызовы: `external_calls_total{service,operation,outcome}` и гистограмма длительности; `outcome` из `ok|error|timeout`.
- **Секреты:** коды, токены сессий, ключи API и тела писем не логируются; middleware маскирует поля `code`, `token`, `apiKey`, `authorization`.

## Codex app-server и модели

- **Версия:** `@openai/codex@0.163.0` (15 октября 2026; spike работал на 0.155.1, последний релиз 0.165.0 вышел 27 октября и пока не берём). Протокол проверяется на этой версии в тестах адаптера. Образ на основе Dockerfile spike с `ca-certificates`.
- **Авторизация ключом API.** После `initialize` адаптер вызывает `account/login/start` с `{"type":"apiKey","apiKey":…}`, затем проверяет `account/read` → `account.type == "apiKey"` ([app-server](https://learn.chatgpt.com/docs/app-server), [auth](https://learn.chatgpt.com/docs/auth)). Ключ передаётся в контейнер через stdin-протокол, на диск в репозиторий не пишется; `CODEX_HOME` — временный каталог контейнера. Лимит расходов выставляется на стороне проекта OpenAI.
- **Модель задаётся** параметром `model` в `thread/start` (дефолт сессии) и при необходимости в `turn/start`; там же `effort` и `outputSchema` для структурированного ответа (действует на один turn). Структурированные ответы ревьюеров проверяются по `contracts/agent-result.schema.json`.
- **Изоляция ролей:** отдельный контейнер и `CODEX_HOME` на сессию; генерация получает свежую рабочую папку только с утверждённым пакетом; Учебному наставнику папка курса монтируется Docker-ом `:ro`. Запрет записи обеспечивает Docker, а не промпт.
- **Модели по ролям** ([каталог моделей](https://developers.openai.com/api/docs/models)), задаются переменными окружения `MODEL_<ROLE>`:

| Роль | Модель | Почему |
|---|---|---|
| Наставник-планировщик | `gpt-6-astra` | флагман, интервью и план требуют рассуждений |
| Ревьюер плана | `gpt-6-astra` | та же требовательность к цели и плану |
| Генератор курса | `gpt-6.1-sol` | проще и дешевле планировщика, объём текста большой |
| Учебный наставник | `gpt-6.1-sol` | диалог по готовым материалам |
| Ревьюер работ | `gpt-6-astra` | от точности оценивания зависит зачёт |

  В примере retrieve-model документации у `gpt-6-astra` указан `shutdown_date` 2026-10-23; перед live-прогоном `make verify-live` проверяет доступность всех ID через `GET /v1/models` и падает с понятной ошибкой.

## GitHub

- **Механизм:** одна GitHub App, установленная в организацию платформы (GitHub Free). Серверные действия — installation access token (IAT); привязка аккаунта ученика — OAuth user-to-server той же App (`client_id`/`client_secret`, callback `http://localhost:<port>/api/github/callback`), сохраняем `github_user_id` и `login`, токен ученика дальше не используем.
- **Права App** ([permissions for GitHub Apps](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps)): Administration write (generate из шаблона, приглашение коллаборатора), Contents write (чтение шаблона, tarball коммита, merge), Pull requests write (reviews), Checks write (check runs), Metadata read. Организации: Members read не нужен.
- **Создание репозитория:** `POST /repos/{org}/{template}/generate` с `private: true` и детерминированным именем `course-{courseId}`. Идемпотентность: перед созданием и при ошибке/тайм-ауте — `GET /repos/{org}/course-{courseId}`; 422 «name already exists» трактуется как «найти существующий». Содержимое по плану коммитится поверх шаблона через Contents API.
- **Доступ ученика:** `PUT /repos/{org}/{repo}/collaborators/{login}` с `permission: push` создаёт приглашение; статус ждём по `GET /repos/{org}/{repo}/invitations` и `GET /repos/{org}/{repo}/collaborators/{login}`. На GitHub Free у организаций приватные репозитории и outside collaborators не ограничены и не тарифицируются ([outside collaborators](https://docs.github.com/en/organizations/managing-user-access-to-your-organizations-repositories/managing-outside-collaborators/adding-outside-collaborators-to-repositories-in-your-organization), [changelog 2020](https://github.blog/changelog/2020-04-14-private-repositories-with-unlimited-collaborators-available-to-all-github-accounts-and-changes-to-github-paid-plans/)).
- **Approve от App: да.** `POST /repos/{owner}/{repo}/pulls/{n}/reviews` с `event: APPROVE|REQUEST_CHANGES|COMMENT` доступен IAT при Pull requests write ([reviews API](https://docs.github.com/en/rest/pulls/reviews)). Автор PR — ученик, поэтому запрет самоодобрения не мешает. Ревью публикуется с `commit_id` проверенного коммита.
- **Merge:** `PUT /repos/{owner}/{repo}/pulls/{n}/merge` с `sha` = проверенный head. Если head сменился, GitHub отвечает 409; это и есть защита «новый коммит не объединяется по старым результатам». 405/409 — состояние «merge не выполнен».
- **Ограничение Free:** защита веток и rulesets для приватных репозиториев бесплатной организации не применяются ([обсуждение](https://github.com/orgs/community/discussions/174400)). Ученик с push может сам смержить PR или пушнуть в `main`. Поэтому зачёт определяется только записью в нашей БД (официальная проверка + approve для одного SHA), а не фактом merge в GitHub; самостоятельный merge учеником зачёта не даёт и показывается как есть.
- **Вебхуки локально:** smee.io-канал как Webhook URL App и `smee-client` в compose с `--target http://backend:8080/api/github/webhook` (рекомендация [GitHub Docs](https://docs.github.com/en/apps/creating-github-apps/writing-code-for-a-github-app/building-a-github-app-that-responds-to-webhook-events)). Подпись `X-Hub-Signature-256` проверяется. Идемпотентность по `X-GitHub-Delivery` (уникальный ключ в БД) и по `(repo, pr, head_sha)` для эффектов. Повторную доставку делаем через «Redeliver» в настройках App. События: `pull_request` (opened, synchronize, reopened), `installation`, `member`/`repository_invitation` не нужны — статус приглашения опрашиваем.

## Официальные проверки

- Тесты задания лежат в версии курса в S3 под `private/checks/{taskId}/` и в репозиторий ученика не попадают.
- Запуск на событие `pull_request` для `head_sha`: бэкенд создаёт check run `in_progress` (`POST /repos/{o}/{r}/check-runs`, `head_sha`, `external_id` = ID попытки), скачивает tarball ровно этого SHA (`GET /repos/{o}/{r}/tarball/{sha}`) и запускает одноразовый контейнер:
  `docker run --rm --network none --read-only --tmpfs /tmp --memory 512m --cpus 1 --pids-limit 256 --user 65534 -v <код>:/work/src:ro -v <checks>:/work/checks:ro <runner-image>` без переменных окружения платформы и без Docker socket.
- Runner-образ `infra/check-runner/`: Python 3.13 + pytest для Python; для Git — тот же образ с `git`, проверки читают `.git` из tarball-копии клона (для Git-заданий скачиваем bare-клон по IAT на хосте, в контейнер попадает только копия). Workflow и тесты из репозитория ученика игнорируются: запускается только `/work/checks`.
- Итог → `PATCH check-runs/{id}`: `conclusion` `success|failure`, краткий вывод (имена упавших проверок и сообщения assert без исходника служебных тестов). Таймаут или сбой инфраструктуры → `conclusion: neutral` с текстом «техническая ошибка» и статус попытки `error`, а не `failure`.
- Ключ App и IAT живут только в процессе бэкенда; контейнер проверки их не видит.

## Посещаемость

- Посещение — любой аутентифицированный запрос ученика (не преподавателя), строка `(user_id, at timestamptz)`.
- Часовой пояс — `Europe/Moscow` (UTC+3, без перехода на летнее время). День — календарный день в этом поясе.
- Окно для «сейчас» в день D: `[00:00 D-6, 00:00 D+1)` по Москве, то есть семь календарных дней D-6…D включительно. Регулярно активный — минимум два различных дня `date(at AT TIME ZONE 'Europe/Moscow')` в окне. Всего учеников — уникальные `user_id` за всё время.
- Время в тестах подаётся параметром `now`, а не берётся из часов.

## Тайм-ауты и опрос

| Операция | Тайм-аут | При превышении |
|---|---|---|
| Ход модели в интервью и чате | 120 с | ошибка операции, сообщение ученику с кнопкой «Повторить» |
| Ревью плана, ревью работ, проверка короткого ответа | 180 с | ошибка, не зачёт и не одобрение |
| Генерация курса целиком | 30 мин | ошибка генерации, ручной «Повторить» |
| Официальная проверка | 120 с на контейнер | `neutral` + «техническая ошибка» |
| Вызов GitHub API | 30 с | ошибка операции, повтор идемпотентен |

Сайт опрашивает статус фоновой операции и попытки каждые 3 с, пока статус не финальный. Целевое время появления результата CI на сайте — до 10 с после завершения проверки. Это ориентир, не порог приёмки.

## Конфигурация и секреты

Все секреты и идентификаторы передаются через `.env` (в `.gitignore`), шаблон без значений — `.env.example`. В документах и логах значений нет.

## Недостающие доступы

Ключ OpenAI уже есть в `.env` (`OPENAI_API_KEY`), повторно не нужен. Нужно от пользователя, интерактивным скриптом (`/wizard`) при работе над #17:

1. **Организация GitHub платформы** (Free достаточно): имя организации, `GITHUB_ORG`.
2. **GitHub App**, созданная в этой организации и установленная в неё на все репозитории, с правами из раздела «GitHub» и событием `pull_request`:
   - `GITHUB_APP_ID`;
   - приватный ключ App (`.pem`), путь в `GITHUB_APP_PRIVATE_KEY_PATH`;
   - `GITHUB_APP_CLIENT_ID` и `GITHUB_APP_CLIENT_SECRET` для OAuth, callback `http://localhost:8080/api/github/callback`;
   - `GITHUB_WEBHOOK_SECRET`;
   - URL канала smee.io, `SMEE_URL`, указанный как Webhook URL App.
3. **Два тестовых GitHub-аккаунта учеников** для проверки «ученик B не читает репозиторий A» (AC-03, AC-05).
4. **Лимит расходов** на проект OpenAI, которому принадлежит ключ: подтверждение, что он выставлен.

Без пунктов 1–3 live-проверки GitHub (AC-03 в части репозитория, AC-05, AC-14 для Python и Git) остаются непройденными; локальные тесты обработчиков от них не зависят.
