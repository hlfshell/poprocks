# Request / Response

## Chosen approach

The protocol supports two first-class patterns over the same `vsock`
connection:

- `Send` for one-way messages
- `Request` for request/response flows

Typed wrappers mirror that split:

- `M[T]` for one-way messages
- `R[Req, Resp]` for request/response interactions

## One-way messages

Use `M[T]` when the sender does not need an application-level result.

```go
eventCodec, _ := NewCodecOfType[VMEvent](1001, CodecMsgpack)
events, _ := NewM[VMEvent](messenger, eventCodec)

events.OnReceive(func(evt VMEvent) {
	log.Printf("event: %+v", evt)
})

_ = events.Send(ctx, VMEvent{Name: "started"})
```

## Request / response

Use `R[Req, Resp]` when the sender expects a typed response.

```go
reqCodec, _ := NewCodecOfType[StartVM](2001, CodecMsgpack)
respCodec, _ := NewCodecOfType[StartVMResult](2002, CodecMsgpack)

control, _ := NewR[StartVM, StartVMResult](messenger, reqCodec, respCodec)

control.OnRequest(func(ctx context.Context, req StartVM) (StartVMResult, error) {
	return StartVMResult{OK: true}, nil
})

resp, err := control.Request(ctx, StartVM{ID: "vm-1"})
```

## Correlation

Requests and responses are correlated with the existing 8-byte message ID.

- request gets message ID `x`
- response is sent back with message ID `x`
- requester resolves the waiting response using that ID

The request and response still use distinct 4-byte message type IDs.

## Semantics

### `Send`

- one-way
- no typed application response
- good for events, notifications, streamed bodies

### `Request`

- typed request
- typed response
- timeout comes from the request context
- intended for operations that need a meaningful outcome

## Relationship to acks

Transport acknowledgements may still exist as a lower-level reliability detail,
but `Request` is the preferred mechanism when the sender needs an actual result.

That means the application-level meaning should come from the typed response,
not from transport acknowledgement alone.

## File transfer fit

This model works well for file transfer setup:

1. `Request(FileTransferStart) -> FileTransferAccepted`
2. stream file body via one-way stream message
3. optionally send a final completion result

That keeps metadata/result handling separate from bulk streaming.
