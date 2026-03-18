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

type Messenger struct {
	vsock net.Conn

	lock      sync.RWMutex
	writeLock sync.Mutex
	hbLock    sync.Mutex

	config MessengerConfig

	receivers              map[uint32]func(context.Context, *Message) error
	unknownMessageReceiver func(context.Context, *Message) error

	heartbeat        Heartbeat
	heartbeatPending chan struct{}
	heartbeatStarted bool
	heartbeatHealth  func() map[string]any
}

type MessengerConfig struct {
	// RequireAcknowledge is a flag that indicates whether the messenger should
	// require an acknowledgment from the receiver.
	RequireAcknowledge bool
	// These are only utilized if RequireAcknowledge is true
	Timeout    time.Duration
	MaxRetries int
	// MaximumMessageSize in bytes - if 0 then no limit
	// is set. This is set to prevent a malicious actor from sending a
	// large binary payload that could cause issues or be an unexpected
	// payload type.
	MaxMessageSize int
	// MaximumMessageSizeReceived in bytes - if 0 then no limit. Like
	// MaxMessageSize, this is set to prevent a malicious actors, but
	// specifically targets received messages (thus not limiting this host from
	// sending).
	MaxMessageSizeReceived int

	// Heartbeat enables/disables heartbeat mechanisms to ensure the client is
	// alive and responsive. Can also be utilized as a health check for the
	// service.
	Heartbeat bool
	// HeartbeatHost determines if this messenger is the host to send heartbeats
	// or is a client that receives heartbeats.
	HeartbeatHost bool
	// The following are only utilized if Heartbeat is true
	HeartbeatInterval time.Duration
	HeartbeatTimeout  time.Duration
}

const (
	DefaultTimeout           = 5 * time.Second
	DefaultMaxRetries        = 3
	DefaultMaxMessageSize    = 4 * 1024 * 1024
	DefaultHearatbeat        = false
	DefaultHeartbeatHost     = true
	DefaultHeartbeatInterval = 15 * time.Second
	DefaultHeartbeatTimeout  = 45 * time.Second
	AbsoluteMaxMessageSize   = 64 * 1024 * 1024
)

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

	if c.Heartbeat {
		if c.HeartbeatInterval <= 0 {
			return fmt.Errorf("heartbeat interval must be > 0 when heartbeat is enabled")
		}
		if c.HeartbeatTimeout <= 0 {
			return fmt.Errorf("heartbeat timeout must be > 0 when heartbeat is enabled")
		}
		if c.HeartbeatTimeout <= c.HeartbeatInterval {
			return fmt.Errorf("heartbeat timeout must be greater than heartbeat interval")
		}
	}
	return nil
}

func NewMessenger(connection net.Conn) *Messenger {
	cfg := MessengerConfig{
		RequireAcknowledge:     false,
		Timeout:                DefaultTimeout,
		MaxRetries:             DefaultMaxRetries,
		MaxMessageSize:         DefaultMaxMessageSize,
		MaxMessageSizeReceived: DefaultMaxMessageSize,
		Heartbeat:              DefaultHearatbeat,
		HeartbeatHost:          DefaultHeartbeatHost,
		HeartbeatInterval:      DefaultHeartbeatInterval,
		HeartbeatTimeout:       DefaultHeartbeatTimeout,
	}

	// If we don't specify it behavior for it, we ignore unknown messages
	onUnknown := func(ctx context.Context, msg *Message) error {
		return nil
	}

	return &Messenger{
		config:                 cfg,
		receivers:              make(map[uint32]func(context.Context, *Message) error),
		vsock:                  connection,
		unknownMessageReceiver: onUnknown,
	}
}

func (m *Messenger) OnUnknown(receiver func(context.Context, *Message) error) error {
	if receiver == nil {
		return ErrNilHandler
	}
	m.lock.Lock()
	m.unknownMessageReceiver = receiver
	m.lock.Unlock()
	return nil
}

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

func (m *Messenger) OnHeartbeatHealth(provider func() map[string]any) {
	m.hbLock.Lock()
	m.heartbeatHealth = provider
	m.hbLock.Unlock()
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
	if msg.Type == heartbeatTypeID {
		inMemory, err := materializeStreamMessage(msg)
		if err != nil {
			return err
		}
		return m.handleHeartbeatMessage(ctx, inMemory)
	}

	m.lock.RLock()
	handler := m.receivers[msg.Type]
	unknown := m.unknownMessageReceiver
	m.lock.RUnlock()
	if handler != nil {
		return handler(ctx, msg)
	}
	return unknown(ctx, msg)
}

func (m *Messenger) readMessage() (*Message, error) {
	header, err := m.readHeader()
	if err != nil {
		return nil, err
	}
	return m.readMessagePayload(header)
}

func (m *Messenger) Serve(ctx context.Context) error {
	if m == nil || m.vsock == nil {
		return ErrNilTransport
	}

	if m.config.Heartbeat && m.config.HeartbeatHost {
		m.StartHeartbeat(ctx)
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

func (m *Messenger) readMessagePayload(header *Message) (*Message, error) {
	if header == nil {
		return nil, ErrNilMessage
	}
	return materializeStreamMessage(newMessageFromHeader(header, m.vsock))
}
