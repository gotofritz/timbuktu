# Go Maps

Maps are unordered collections of key-value pairs. They provide O(1) average-case lookup.

## Growth

A map grows as you add more keys. The runtime handles this transparently.
When a map's load factor gets too high, the runtime rehashes the entire map.

Maps grow by reallocating buckets and re-hashing all entries into the new bucket array.
This is more complex than slice growth because each key needs to be re-hashed.

## Usage

```go
m := make(map[string]int)
m["key"] = 42
value, ok := m["key"]
```

Maps are implemented as hash tables internally, with buckets and overflow chains.
