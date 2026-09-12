# Go Channels

A channel carries values between goroutines. `make(chan int)` is unbuffered:
every send blocks until another goroutine receives.

## Buffer capacity

`make(chan int, 8)` gives the channel a buffer of capacity 8. `cap` reports the
buffer size and `len` reports how many values are waiting in it. A send blocks
only once the buffer is full.

The capacity is fixed when the channel is made. A channel never grows its
buffer, so a producer outrunning its consumer blocks rather than allocating.

## Closing

Receiving from a closed channel yields the zero value immediately. The two
value form, `v, ok := <-ch`, reports whether the channel was still open.
