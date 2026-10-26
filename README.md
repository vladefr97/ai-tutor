# tutor-app

Текущий проект — отдельный сайт **ai-tutor** на React и Go. Согласованные требования: [ADR-0002](docs/adr/0002-otdelnyj-sajt-dlya-poc.md), [критерии приёмки](docs/poc-acceptance.md), [план реализации](docs/superpowers/plans/2026-10-25-ai-tutor-poc.md). Новое приложение ещё не реализовано; ниже сохранено описание прежнего Claude-прототипа.

## Прежний Claude-прототип

Плагин Claude: по запросу «хочу освоить …» проводит интервью, формирует бриф, пишет курс (темы со шпаргалками и заданиями) и публикует плеер, в котором ученик проходит курс вместе с Наставником. Решение об архитектуре: `docs/adr/0001-plagin-claude-vmesto-svoego-servisa.md`. Термины: `GLOSSARY.md`.

Репозиторий одновременно плагин (`.claude-plugin/plugin.json`) и marketplace (`.claude-plugin/marketplace.json`).

## Установка

Без терминала, через интерфейс Claude. Названия пунктов меню меняются, ниже ориентиры; точные подписи не проверены.

1. Откройте настройки Claude (Claude Desktop или claude.ai) и найдите раздел плагинов (Plugins или Customize).
2. Добавьте marketplace из GitHub: укажите репозиторий `vladefr97/tutor-app`. Для приватного репозитория нужно разрешить Claude доступ к нему через GitHub-интеграцию.
3. Установите из этого marketplace плагин `tutor`.
4. Плагин привязан к аккаунту и должен появиться на других устройствах с тем же аккаунтом. Проверка: в Claude Code на втором ноутбуке виден `/grilling`.

Обновление: после новых изменений в репозитории обновите marketplace в том же разделе.

## Как пользоваться

В Claude Desktop, вкладка Code, откройте пустую папку проекта и напишите: «Хочу освоить …». Для интервью и плана выберите в приложении модель Opus: тему и план пишет модель чата, сами темы пишет субагент `writer` на Sonnet.

## Лицензии бандлированных скиллов

| Скилл | Источник | Лицензия |
|---|---|---|
| `grilling` | https://github.com/mattpocock/skills (`skills/productivity/grilling`) | MIT, © 2026 Matt Pocock. Проверено по LICENSE репозитория через GitHub API |
| `humanizer-ru` | https://github.com/smixs/humanizer-ru | MIT, © 2026 Serge Shima. Текст лицензии лежит в `skills/humanizer-ru/LICENSE` |
| `ru-text` | https://github.com/talkstream/ru-text (версия 2.7.0) | MIT, © 2026 Arseniy Kamyshev (по LICENSE пакета) |

Скиллы скопированы без изменений. Лицензия grilling проверена на уровне репозитория-источника; отдельного файла лицензии в папке скилла нет.

## Локальная сборка плеера

Проверить папку курса и собрать плеер без агента:

```
python3 player/build.py <папка-курса> out.html
```

Нужен только Python 3 без зависимостей. При ошибке скрипт печатает путь файла и причину и завершается с ненулевым кодом.

## Публикация плеера

Публикует скилл `tutor` в конце работы: собирает `player.html` скриптом сборки и публикует его инструментом Artifact с возможностями `sample`, `db`, `user`. Ссылка приходит в чат один раз, когда курс готов целиком.

Вручную, одной командой: `python3 player/build.py <папка-курса> player.html`, затем публикация файла инструментом Artifact с `capabilities: {sample: {}, db: {}, user: {}}`. Прогресс темы лежит в документе `data/users/<id>/progress-<topicId>`, без базы страница работает на `localStorage`.

## Тесты

```
python3 -m unittest discover player   # сборка и валидация
node --test player/grade.test.js      # grade()
```

Установка в Claude Code: `/plugin marketplace add vladefr97/tutor-app`, затем `/plugin install tutor@tutor-app`.

Профиль ученика копируется в папку курса при её создании: правка `learner.json` в корне проекта не меняет уже созданные курсы.

## Известные ограничения проверки

- В headless-режиме `claude -p` у скилла нет инструмента Artifact: курс собирается до `player.html`, публикация не выполняется, скилл сообщает путь к файлу. Публикация проверяется только в Claude Desktop → Code.
