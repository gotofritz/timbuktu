# Go Slices

A slice is a dynamically-sized, flexible view into the elements of an array.

## Growth

When you append to a slice, Go reallocates the underlying array when len == cap.
The capacity is roughly doubled, following the growth pattern:

- If cap < 1024, double it
- If cap >= 1024, grow by 25% (multiply by 1.25)

This is handled by the runtime's append built-in function.

## Internals

Internally, a slice is a struct:

```go
type slice struct {
    array *[...]T
    len   int
    cap   int
}
```

The array pointer, length, and capacity together define the slice's view.
