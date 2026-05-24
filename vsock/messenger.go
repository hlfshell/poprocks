package vsock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Messenger handles  connceting to and managing a vsock messenger for high
// speed communication between host and client processes. It is configurable and
// provides sufficient protections for smooth transfer of data from small
// messages to large binary files. It can handle configurable ACKs and correctly
// routes messages based on ID.
type Messenger struct {
	vsock net.Conn

	lock      sync.RWMutex
	writeLock sync.Mutex
	ackLock   sync.Mutex
	respLock  sync.Mutex

	config MessengerConfig

	receivers              map[uint32]func(context.Context, *Message) error
	unknownMessageReceiver func(context.Context, *Message) error
	pendingAcks            map[uint64]chan struct{}
	pendingResponses       map[uint64]pendingResponse
}

type MessengerConfig struct {
	// RequireAcknowledge is a flag that indicates whether the messenger should
	// require an acknowledgment from the receiver.
	RequireAcknowledge bool
	// These are only utilized if RequireAcknowledge is true
	Timeout    time.Duration
	MaxRetries int
	// MaximumMessageSize in bytes - if 0 then no limit is set. This is set to
	// prevent a malicious actor from sending a large binary payload that could
	// cause issues or be an unexpected payload type.
	MaxMessageSize int
	// MaximumMessageSizeReceived in bytes - if 0 then no limit. Like
	// MaxMessageSize, this is set to prevent a malicious actors, but
	// specifically targets received messages (thus not limiting this host from
	// sending).
	MaxMessageSizeReceived int
}

const (
	DefaultTimeout         = 5 * time.Second
	DefaultMaxRetries      = 3
	DefaultMaxMessageSize  = 4 * 1024 * 1024
	AbsoluteMaxMessageSize = 64 * 1024 * 1024
)

func DefaultMessengerConfig() MessengerConfig {
	return MessengerConfig{
		RequireAcknowledge:     false,
		Timeout:                DefaultTimeout,
		MaxRetries:             DefaultMaxRetries,
		MaxMessageSize:         DefaultMaxMessageSize,
		MaxMessageSizeReceived: DefaultMaxMessageSize,
	}
}

func (c *MessengerConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("max retries must be >= 0")
	}
	if c.MaxMessageSize < 0 || c.MaxMessageSizeReceived < 0 {
		return fmt.Errorf("message size limits must be >= 0")
	}
	if c.MaxMessageSize > AbsoluteMaxMessageSize || c.MaxMessageSizeReceived > AbsoluteMaxMessageSize {
		return fmt.Errorf("message size exceeds hard limit of %d bytes", AbsoluteMaxMessageSize)
	}

	if c.RequireAcknowledge && c.Timeout <= 0 {
		return fmt.Errorf("timeout must be > 0 when acknowledgements are required")
	}

	return nil
}

func NewMessenger(connection net.Conn) *Messenger {
	return newMessenger(connection, DefaultMessengerConfig())
}

func NewMessengerWithConfig(connection net.Conn, cfg MessengerConfig) (*Messenger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return newMessenger(connection, cfg), nil
}

func newMessenger(connection net.Conn, cfg MessengerConfig) *Messenger {
	return &Messenger{
		config:           cfg,
		receivers:        make(map[uint32]func(context.Context, *Message) error),
		pendingAcks:      make(map[uint64]chan struct{}),
		pendingResponses: make(map[uint64]pendingResponse),
		vsock:            connection,
	}
}

// OnUnknown allows you to receive and deal with messages that are not
// identified to the vsock messenger connection. By default they are ignored.
func (m *Messenger) OnUnknown(receiver func(context.Context, *Message) error) error {
	if receiver == nil {
		return ErrNilHandler
	}
	m.lock.Lock()
	m.unknownMessageReceiver = receiver
	m.lock.Unlock()
	return nil
}

// OnReceive specifies a singular handler to react when a given message of a
// known type is received.
func (m *Messenger) OnReceive(msgType uint32, receiver func(context.Context, *Message) error) error {
	if receiver == nil {
		return ErrNilHandler
	}
	if msgType == 0 {
		return ErrInvalidTypeID
	}

	m.lock.Lock()
	defer m.lock.Unlock()
	if _, exists := m.receivers[msgType]; exists {
		return ErrHandlerAlreadyRegistered
	}
	m.receivers[msgType] = receiver
	return nil
}

func (m *Messenger) Send(ctx context.Context, msg *Message) error {
	if msg == nil {
		return ErrNilMessage
	}
	if m.vsock == nil {
		return ErrNilTransport
	}
	payload, err := msg.ReadAll()
	if err != nil {
		return err
	}
	return m.SendStreamWithID(ctx, msg.ID, msg.Type, uint32(len(payload)), bytes.NewReader(payload))
}

func (m *Messenger) handleMessage(ctx context.Context, msg *Message) error {
	if msg == nil {
		return ErrNilMessage
	}
	if resolved, err := m.resolveResponse(msg); err != nil {
		return err
	} else if resolved {
		return m.sendAcknowledge(ctx, msg)
	}
	if msg.Type == acknowledgeTypeID {
		m.resolveAck(msg.ID)
		return nil
	}

	m.lock.RLock()
	handler := m.receivers[msg.Type]
	unknown := m.unknownMessageReceiver
	m.lock.RUnlock()
	if handler != nil {
		if err := handler(ctx, msg); err != nil {
			return err
		}
		return m.sendAcknowledge(ctx, msg)
	}
	if unknown != nil {
		if err := unknown(ctx, msg); err != nil {
			return err
		}
	}
	return m.sendAcknowledge(ctx, msg)
}

func (m *Messenger) Serve(ctx context.Context) error {
	if m == nil || m.vsock == nil {
		return ErrNilTransport
	}

	stopReadDeadline := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = m.vsock.SetReadDeadline(time.Now())
		case <-stopReadDeadline:
		}
	}()
	defer close(stopReadDeadline)

	for {
		header, err := m.readHeader()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() && ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		streamMsg := newMessageFromHeader(header, m.vsock)
		err = m.handleMessage(ctx, streamMsg)
		drainErr := streamMsg.drain()
		if err != nil {
			return err
		}
		if drainErr != nil {
			return drainErr
		}
	}
}
