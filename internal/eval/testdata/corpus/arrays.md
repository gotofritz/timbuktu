# Go Arrays

An array has a fixed length that is part of its type: `[5]int` and `[6]int` are
different types. The length is known at compile time and cannot change.

## Capacity

An array's capacity always equals its length. There is nothing to grow into,
which is why `append` works on slices and not on arrays.

## Assignment

Assigning an array copies every element, because an array is a value and not a
view. Passing a large array to a function copies it too, which is why most Go
code passes a slice instead.
