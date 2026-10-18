#!/usr/bin/env python3
"""Собирает HTML-плеер из папки курса: build.py <папка-курса> [out.html].
Промежуточная проверка без сборки: build.py <папка-курса> --upto N (проверить course.json, learner.json и первые N тем; 0 — только их)."""
import json
import sys
from pathlib import Path

TEMPLATE = Path(__file__).with_name("template.html")
GRADE_JS = Path(__file__).with_name("grade.js")
LEARNER_KEYS = ["name", "address", "tone", "emoji", "format", "mentor", "wishes"]
KINDS = {"quiz", "order", "open"}


class BuildError(Exception):
    def __init__(self, path, msg):
        super().__init__(f"{path}: {msg}")


def load_json(path):
    if not path.is_file():
        raise BuildError(path, "файл не найден")
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except ValueError as e:
        raise BuildError(path, f"невалидный JSON ({e})")


def need(cond, path, msg):
    if not cond:
        raise BuildError(path, msg)


def check_task(task, path):
    need(isinstance(task, dict), path, "задание должно быть объектом")
    for key in ("id", "kind", "q"):
        need(isinstance(task.get(key), str) and task[key], path, f"нет поля «{key}»")
    kind = task["kind"]
    tid = task["id"]
    need(kind in KINDS, path, f"задание {tid}: неизвестный kind «{kind}»")
    if kind in ("quiz", "order"):
        need(isinstance(task.get("why"), str) and task["why"], path, f"задание {tid}: нет «why»")
    if kind == "quiz":
        opts = task.get("opts")
        need(isinstance(opts, list) and len(opts) >= 2, path, f"задание {tid}: в «opts» меньше двух вариантов")
        ans = task.get("answer")
        need(isinstance(ans, int) and not isinstance(ans, bool) and 0 <= ans < len(opts),
             path, f"задание {tid}: «answer» вне «opts»")
    elif kind == "order":
        items = task.get("items")
        need(isinstance(items, list) and len(items) >= 3, path, f"задание {tid}: в «items» меньше трёх элементов")
    else:
        need(isinstance(task.get("rubric"), str) and task["rubric"].strip(), path, f"задание {tid}: нет «rubric»")


def load_course(root, upto=None):
    root = Path(root)
    course = load_json(root / "course.json")
    cpath = root / "course.json"
    need(isinstance(course, dict), cpath, "ожидался объект")
    for key in ("title", "brief"):
        need(isinstance(course.get(key), str) and course[key], cpath, f"нет поля «{key}»")
    need(course.get("practice") == "text", cpath, "«practice» должно быть «text»")
    topics = course.get("topics")
    need(isinstance(topics, list) and topics, cpath, "нет списка «topics»")

    learner = load_json(root / "learner.json")
    need(isinstance(learner, dict), root / "learner.json", "ожидался объект")
    for key in LEARNER_KEYS:
        need(key in learner, root / "learner.json", f"нет поля «{key}»")

    seen, out = set(), []
    for i, t in enumerate(topics, 1):
        need(isinstance(t, dict), cpath, f"тема №{i} должна быть объектом")
        for key in ("id", "slug", "title"):
            need(isinstance(t.get(key), str) and t[key], cpath, f"тема №{i}: нет поля «{key}»")
        need(isinstance(t.get("final"), bool), cpath, f"тема №{i}: «final» должно быть true/false")
        need(t["id"] not in seen, cpath, f"дубликат id темы «{t['id']}»")
        seen.add(t["id"])
        need(t["final"] == (i == len(topics)), cpath, "итоговая тема должна быть одна и последняя")
        if upto is not None and i > upto:
            continue

        tdir = root / "topics" / f"{i:02d}-{t['slug']}"
        tpath = tdir / "tasks.json"
        tasks = load_json(tpath)
        need(isinstance(tasks, list) and tasks, tpath, "ожидался непустой список заданий")
        ids = set()
        for task in tasks:
            check_task(task, tpath)
            need(task["id"] not in ids, tpath, f"дубликат id задания «{task['id']}»")
            ids.add(task["id"])

        cheat = None
        if not t["final"]:
            cpath_md = tdir / "cheatsheet.md"
            need(cpath_md.is_file(), cpath_md, "нет шпаргалки")
            cheat = cpath_md.read_text(encoding="utf-8")
            need(cheat.strip(), cpath_md, "шпаргалка пуста")
        out.append({**t, "cheatsheet": cheat, "tasks": tasks})
    return {**course, "learner": learner, "topics": out}


def build(root):
    data = json.dumps(load_course(root), ensure_ascii=False).replace("</", "<\\/")
    html = TEMPLATE.read_text(encoding="utf-8").replace("/*GRADE_JS*/", GRADE_JS.read_text(encoding="utf-8"))
    return html.replace("/*COURSE_JSON*/null", data)


def main(argv):
    upto = None
    if len(argv) == 4 and argv[2] == "--upto" and argv[3].isdigit():
        upto = int(argv[3])
    elif not 2 <= len(argv) <= 3 or "--upto" in argv:
        print(__doc__, file=sys.stderr)
        return 2
    try:
        if upto is not None:
            load_course(argv[1], upto)
            print(f"Проверено тем: {upto}")
            return 0
        html = build(argv[1])
    except BuildError as e:
        print(f"Ошибка: {e}", file=sys.stderr)
        return 1
    out = Path(argv[2] if len(argv) == 3 else "player.html")
    out.write_text(html, encoding="utf-8")
    print(f"Готово: {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
