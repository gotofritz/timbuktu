# Allocation and the Go Heap

Go decides between the stack and the heap by escape analysis: a value whose
reference outlives the function that made it is allocated on the heap.

## Garbage collection

The collector is a concurrent mark-and-sweep. `GOGC` sets the target heap
growth between collections — at the default of 100, a cycle starts once the
live heap has doubled since the last one.

## Reallocating

Growing a data structure usually means allocating a new, larger backing array
and copying into it. The old array becomes garbage and is reclaimed by the next
collection.
