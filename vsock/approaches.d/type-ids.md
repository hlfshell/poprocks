# Type IDs

## Chosen approach

Typed wrappers require an explicit codec.

`NewM[T]` no longer accepts a `nil` codec and no longer auto-generates message
type IDs from Go type names.

Example:

```go
const TypeCreateUser uint32 = 1001

codec, err := NewCodecOfType[CreateUser](TypeCreateUser, CodecJSON)
if err != nil {
	return err
}

wrapper, err := NewM[CreateUser](messenger, codec)
if err != nil {
	return err
}
```

## Wire format

This decision does **not** change the wire size of the message type field.

The framed header still uses:

- 8-byte message ID (`uint64`)
- 4-byte message type (`uint32`)
- 4-byte payload length (`uint32`)

So the explicit type ID callers provide is still the existing 4-byte `uint32`
type value, not a new 8-byte field.

## Why this was chosen

Auto-generated IDs were convenient, but they tied protocol meaning to Go type
names and package layout. That is fragile across:

- refactors
- package renames
- forks
- cross-repo implementations
- non-Go peers

For a shared protocol, it is better for both sides to intentionally agree on a
message type number than to derive it from implementation details.

## What this means in practice

Good:

```go
const TypeInvoiceCreated uint32 = 2001
codec, _ := NewCodecOfType[InvoiceCreated](TypeInvoiceCreated, CodecMsgpack)
wrapper, _ := NewM[InvoiceCreated](messenger, codec)
```

Not allowed anymore:

```go
wrapper, err := NewM[InvoiceCreated](messenger, nil)
```

## Tradeoff

This gives up some local convenience in exchange for a more stable and explicit
wire contract.

It also means accidental collisions become a protocol-governance problem rather
than something hidden behind Go package/type names. That is acceptable because
protocol agreement is the real requirement on a shared connection.
