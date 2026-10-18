// Запуск: node --test player/
const test = require("node:test");
const assert = require("node:assert");
const { grade } = require("./grade.js");

const quiz = { kind: "quiz", opts: ["a", "b", "c"], answer: 1 };
const order = { kind: "order", items: ["x", "y", "z"] };

test("квиз: верный и неверный ответ", () => {
  assert.strictEqual(grade(quiz, 1), true);
  assert.strictEqual(grade(quiz, 0), false);
});

test("порядок: верный и неверный", () => {
  assert.strictEqual(grade(order, ["x", "y", "z"]), true);
  assert.strictEqual(grade(order, ["y", "x", "z"]), false);
});
