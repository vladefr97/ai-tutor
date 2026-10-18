// Чистая проверка квиза и упорядочивания: answer — индекс варианта или массив элементов в порядке ученика.
function grade(task, answer) {
  if (task.kind === "quiz") return answer === task.answer;
  if (task.kind === "order") return answer.length === task.items.length && answer.every((x, i) => x === task.items[i]);
  throw new Error("grade: не поддерживается kind " + task.kind);
}
if (typeof module !== "undefined") module.exports = { grade };
