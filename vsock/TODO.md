TODO

1. Expose messenger configuration through the public API.
The package has a public `MessengerConfig`, but callers cannot currently apply it because `Messenger.config` is private and `NewMessenger` does not accept config input.

2. Done: implement acknowledged delivery semantics.
`RequireAcknowledge`, `Timeout`, and `MaxRetries` are defined and validated, but they are not used in send/receive flow yet.

3. Done: simplify typed receiver semantics around delivery-only callbacks.
`M[T].OnReceive` now uses `func(T)`, runs inline, and treats transport success as successful callback invocation rather than application-level success.

4. Done: require explicit type IDs via non-nil codecs.
`NewM[T]` no longer auto-generates type IDs from Go type names. Callers must provide an explicit codec, and the wire message type remains the existing 4-byte `uint32` header field.

5. Done: remove heartbeat lock reentrancy risk.
The heartbeat health callback is now captured under `hbLock` and invoked after the lock is released.

6. Clarify stream codec expectations at the typed wrapper layer.
Stream transport support is solid at the messenger level, but typed wrappers still materialize payloads unless callers explicitly use `OnReceiveStream`. Decide whether stream codecs should stay bifurcated or support a more consistently streaming typed API.

7. Clean up dead or unused exported errors.
Review whether `ErrInvalidVsockPort`, `ErrNilCodec`, and `ErrUnhandledMessageType` are still intended API surface or should be removed.
