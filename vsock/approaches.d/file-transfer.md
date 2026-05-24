Built-in file transfer sits on top of the existing `R` and `M` wrappers:

- `Request(FileTransferRequest) -> FileTransferResponse` opens a transfer session
- `Send(fileTransferBody)` streams the file bytes without materializing the file in memory
- `Request(FileTransferCommit) -> FileTransferResult` finalizes the transfer, verifies size and checksum, and atomically renames the temp file into place

Receiver policy is intentionally receiver-controlled:

- the receiver chooses the final destination path through `FileTransfer.OnReceive(...)`
- host receivers should ignore any sender-supplied `Destination` and choose a safe host-controlled path, typically with `ResolveHostPathByName(...)`
- guest receivers may honor a host-supplied relative destination, but only under a guest-controlled root, typically with `ResolveSenderPathUnderRoot(...)`

That split prevents a guest from dictating arbitrary host filesystem paths while still allowing the host to direct placement inside the guest.
