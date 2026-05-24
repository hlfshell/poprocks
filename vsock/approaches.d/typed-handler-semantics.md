# Typed Handler Semantics

## Chosen approach

`M[T]` has one receiver slot. The codec owns payload conversion, so the receive
API does not distinguish buffered and stream handlers.

The library contract is:

- decode the frame with the configured codec
- invoke the registered callback
- return the callback error to the messenger

## API shape

`M[T].OnReceive` uses:

```go
func(context.Context, T) error
```

and returns an error:

```go
err := wrapper.OnReceive(func(ctx context.Context, evt Event) error {
	return applyEvent(ctx, evt)
})
```

`RemoveReceiver()` clears the currently registered receiver.

## Execution model

Callbacks run inline on the messenger receive path. The receiver lock is held
only long enough to snapshot the registered callback, then released before
decoding and invocation.

This keeps concurrency explicit: callers that want background processing can
start their own goroutine inside the callback.

## Stream codecs

Stream codecs decode into stream-backed payload values. For example,
`vsock.ReaderPayload` receives the underlying reader and length, while structs
with settable `Reader` and optional `Length` fields can receive custom stream
payloads.

## Ack semantics

Acknowledged delivery means the message reached the registered callback path and
the callback returned nil. Application-level success beyond that is owned by the
callback or a higher-level protocol.
