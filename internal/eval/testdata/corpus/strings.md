# Go Strings

A string is an immutable sequence of bytes. Concatenating in a loop allocates a
new string every iteration, because the old one cannot be extended.

## Building

`strings.Builder` accumulates into an internal byte slice and grows it
amortized, roughly doubling as it fills, so building a string costs a small
number of allocations rather than one per concatenation.

`Grow(n)` reserves capacity up front when the final size is known, which avoids
the intermediate reallocations entirely.

## Runes

Indexing a string yields a byte. Ranging over one yields runes, decoding UTF-8
as it goes.
