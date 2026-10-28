# Практика по Git

Это ваш учебный репозиторий. Задания курса лежат в `tasks/`. Теория — на сайте ai-tutor.

## Подготовка

Установите Git (`git --version`) и один раз представьтесь — эти данные попадут в коммиты:

```sh
git config --global user.name "Ваше имя"
git config --global user.email "почта@example.com"
git config --global init.defaultBranch main
git clone <адрес репозитория с сайта>
cd <имя репозитория>
```

## Как сдать задание

1. Создайте ветку для задания: `git switch -c task-1`.
2. Выполните шаги из `tasks/<задание>/README.md`. Смотрите, что происходит: `git status`, `git log --oneline --graph`.
3. Отправьте ветку: `git push -u origin task-1`.
4. Откройте pull request в GitHub. Сайт покажет результат официальной проверки и замечания ревьюера.

Официальные проверки платформа хранит отдельно и запускает на отправленном коммите. Нажимать Merge не нужно: после зачёта платформа объединит pull request сама.
