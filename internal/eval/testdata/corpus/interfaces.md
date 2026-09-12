# Go Interfaces

An interface value holds a dynamic type and a value. It is nil only when both
halves are nil, which is why a nil pointer stored in an interface is not a nil
interface.

## Method sets

A type's method set decides which interfaces it satisfies. Methods with a
pointer receiver belong to the pointer's method set only, so a value of that
type does not satisfy the interface but its address does.

## Type assertions

`v, ok := x.(T)` reports whether the dynamic type is T without panicking. A
type switch does the same across several types at once.
