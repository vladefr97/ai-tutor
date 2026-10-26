# Adding Two Integers in Python

A function groups instructions into reusable code. Use `def` to define a function, followed by its name and parameters in parentheses. Parameters receive the values you pass when calling the function. Indent the function body, and use `return` to send the result back to the caller.

## Worked example

```python
def sum_integers(first, second):
    return first + second

result = sum_integers(4, 7)
print(result)  # 11
```

Here, `first` receives `4` and `second` receives `7`. The `+` operator adds them, and `return` sends `11` back to be stored in `result`.

## Exercise

Write a function named `add` with two parameters, `a` and `b`. Return their sum using `a + b`. Then test your function:

```python
assert add(2, 3) == 5
```

If the assertion passes, Python continues without an error.
