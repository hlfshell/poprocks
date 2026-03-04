package vsock

import "errors"

var (
	ErrInvalidTypeID    = errors.New("invalid typeID")
	ErrInvalidCodecType = errors.New("invalid codec type")
	ErrInvalidVsockPort = errors.New("invalid vsock port")
	ErrNilTransport     = errors.New("nil transport")
	ErrNilCodec         = errors.New("nil codec")
	ErrNilHandler       = errors.New("nil handler")
	ErrNilMessage       = errors.New("nil message")

	ErrHandlerAlreadyRegistered = errors.New("handler already registered")
	ErrUnhandledMessageType     = errors.New("unhandled message type")
)
