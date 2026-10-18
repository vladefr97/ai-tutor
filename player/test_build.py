"""Запуск: python3 -m unittest discover player"""
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIXTURE = ROOT / "fixtures" / "course"
BUILD = Path(__file__).with_name("build.py")
T1 = "topics/01-chto-takoe-produkt"


def run(course, out):
    return subprocess.run([sys.executable, str(BUILD), str(course), str(out)], capture_output=True, text=True)


def edit_tasks(course, fn, topic=T1):
    p = course / topic / "tasks.json"
    tasks = json.loads(p.read_text(encoding="utf-8"))
    fn(tasks)
    p.write_text(json.dumps(tasks, ensure_ascii=False), encoding="utf-8")


def quiz_answer_out(tasks): tasks[0]["answer"] = 7
def duplicate_id(tasks): tasks[1]["id"] = tasks[0]["id"]
def order_of_two(tasks): tasks[1]["items"] = tasks[1]["items"][:2]
def open_no_rubric(tasks): del tasks[2]["rubric"]


class BuildTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.course = self.tmp / "course"
        shutil.copytree(FIXTURE, self.course)
        self.out = self.tmp / "player.html"

    def assertFails(self, path_part):
        r = run(self.course, self.out)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(path_part, r.stderr)
        self.assertFalse(self.out.exists())

    def test_valid_course(self):
        r = run(self.course, self.out)
        self.assertEqual(r.returncode, 0, r.stderr)
        html = self.out.read_text(encoding="utf-8")
        self.assertIn("Что такое продукт и чем занят продакт", html)
        self.assertIn("Итоговое задание", html)
        self.assertIn("Продукт — это решение чьей-то проблемы", html)
        self.assertNotIn("/*COURSE_JSON*/", html)

    def test_upto_skips_unwritten_topics(self):
        shutil.rmtree(self.course / "topics")
        r = subprocess.run([sys.executable, str(BUILD), str(self.course), "--upto", "0"], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        r = subprocess.run([sys.executable, str(BUILD), str(self.course), "--upto", "1"], capture_output=True, text=True)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("tasks.json", r.stderr)

    def test_upto_zero_checks_course_structure(self):
        p = self.course / "course.json"
        c = json.loads(p.read_text(encoding="utf-8"))
        c["topics"][1]["id"] = c["topics"][0]["id"]
        p.write_text(json.dumps(c, ensure_ascii=False), encoding="utf-8")
        r = subprocess.run([sys.executable, str(BUILD), str(self.course), "--upto", "0"], capture_output=True, text=True)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("course.json", r.stderr)

    def test_missing_cheatsheet(self):
        (self.course / T1 / "cheatsheet.md").unlink()
        self.assertFails("cheatsheet.md")

    def test_answer_out_of_opts(self):
        edit_tasks(self.course, quiz_answer_out)
        self.assertFails("tasks.json")

    def test_duplicate_id(self):
        edit_tasks(self.course, duplicate_id)
        self.assertFails("tasks.json")

    def test_order_of_two(self):
        edit_tasks(self.course, order_of_two)
        self.assertFails("tasks.json")

    def test_open_without_rubric(self):
        edit_tasks(self.course, open_no_rubric)
        self.assertFails("tasks.json")

    def test_missing_learner(self):
        (self.course / "learner.json").unlink()
        self.assertFails("learner.json")


if __name__ == "__main__":
    unittest.main()
