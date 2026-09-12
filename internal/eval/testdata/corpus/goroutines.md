# Goroutines and the Scheduler

A goroutine is a function scheduled by the Go runtime onto an operating system
thread. Starting one costs far less than starting a thread.

## Stack growth

A goroutine starts with a small stack, around two kilobytes. When it needs
more, the runtime allocates a bigger stack, copies the old one into it, and
fixes up the pointers. The stack shrinks again when the goroutine's usage drops.

## GMP

The scheduler multiplexes goroutines (G) onto OS threads (M) through a fixed
number of logical processors (P), set by `GOMAXPROCS`. A blocking syscall hands
the P to another thread so the remaining goroutines keep running.
