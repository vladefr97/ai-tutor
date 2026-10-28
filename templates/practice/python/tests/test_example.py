"""Пример локального теста: проверяет, что окружение настроено. Запуск: python -m pytest"""


def add(a, b):
    return a + b


def test_add():
    assert add(2, 3) == 5
