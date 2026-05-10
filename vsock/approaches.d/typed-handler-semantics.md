# Typed Handler Semantics

## Chosen approach

Typed receivers are treated as delivery callbacks, not application-success boundaries.

The library contract is:

- decode the frame
- route it to the typed receiver(s)
- invoke the registered callback(s)

If that invocation succeeds, the library has done its job.

Application-level failure inside the callback is owned by the callback itself, not by the transport.

## API shape

`M[T].OnReceive` now uses:

```go
func(T)
```

instead of:

```go
func(T) error
```

That keeps the API honest. The old signature implied transport-visible failure handling that did not actually exist.

## Execution model

Typed callbacks run inline and sequentially:

```go
for _, receiver := range typedReceivers {
	receiver(t)
}
return nil
```

This was chosen over background goroutines because it is simpler and avoids hidden concurrency in the library.

## What success means

For typed receivers, success means:

- the payload decoded successfully
- the matching typed callback was invoked

It does **not** mean:

- the callback's business operation succeeded
- downstream persistence succeeded
- the callback's internal retries or side effects completed

Example:

```go
wrapper.OnReceive(func(p Job) {
	if err := processJob(p); err != nil {
		log.Printf("job failed: %v", err)
	}
})
```

In this model, transport success is still true even if `processJob` logs an application failure.

## Ack semantics under this model

Acknowledged delivery should be read as:

> The message was delivered to the typed callback path.

It should **not** be read as:

> The application's business logic completed successfully.

That means the sender can rely on ack/retry for transport-level delivery, but not as proof of application success.

## Why this approach

This choice keeps the system simpler in a few important ways:

- no dropped handler errors, because typed handlers no longer pretend to return meaningful transport errors
- no hidden goroutine fanout in the library
- easier tests and easier reasoning about ordering
- better alignment between API and behavior

## Tradeoffs

### 1. Callback failures are local-only

If a callback fails, that failure must be handled by the callback itself.

Example:

```go
wrapper.OnReceive(func(p Job) {
	if err := writeToDB(p); err != nil {
		metrics.Inc("job_write_failed")
		log.Printf("write failed: %v", err)
	}
})
```

### 2. Slow callbacks block the connection

Because callbacks run inline, a slow typed receiver will block later messages on the same messenger.

Example:

```go
wrapper.OnReceive(func(p Job) {
	time.Sleep(5 * time.Second)
})
```

If a consumer wants background work, it must do that explicitly:

```go
wrapper.OnReceive(func(p Job) {
	go func() {
		if err := processJob(p); err != nil {
			log.Printf("background job failed: %v", err)
		}
	}()
})
```

That makes concurrency an application decision instead of an implicit transport policy.

## Practical guidance

### Good fit

```go
wrapper.OnReceive(func(p Event) {
	cache.Apply(p)
})
```

### Good fit with local failure handling

```go
wrapper.OnReceive(func(p Event) {
	if err := projector.Apply(p); err != nil {
		log.Printf("projection failed: %v", err)
	}
})
```

### If you need application-level success semantics

If a caller needs "work completed successfully" semantics instead of "callback was invoked" semantics, that should be built above `vsock`, not assumed from the transport layer.

Examples:

- an RPC response protocol
- explicit success/failure reply messages
- application-level job state tracking
- idempotent retry logic at the business layer

## Bottom line

The typed wrapper is now intentionally simple:

- `OnReceive(func(T))`
- inline callback invocation
- transport-level delivery semantics only

That keeps `vsock` focused on delivery and dispatch, while leaving business success and failure handling to application code.
