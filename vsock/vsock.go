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
	ackLock   sync.Mutex
	respLock  sync.Mutex

	config MessengerConfig

	receivers              map[uint32]func(context.Context, *Message) error
	unknownMessageReceiver func(context.Context, *Message) error
	pendingAcks            map[uint64]chan struct{}
	pendingResponses       map[uint64]pendingResponse

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

func DefaultMessengerConfig() MessengerConfig {
	return MessengerConfig{
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
	return newMessenger(connection, DefaultMessengerConfig())
}

func NewMessengerWithConfig(connection net.Conn, cfg MessengerConfig) (*Messenger, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return newMessenger(connection, cfg), nil
}

func newMessenger(connection net.Conn, cfg MessengerConfig) *Messenger {
	// If we don't specify it behavior for it, we ignore unknown messages
	onUnknown := func(ctx context.Context, msg *Message) error {
		return nil
	}

	return &Messenger{
		config:                 cfg,
		receivers:              make(map[uint32]func(context.Context, *Message) error),
		pendingAcks:            make(map[uint64]chan struct{}),
		pendingResponses:       make(map[uint64]pendingResponse),
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

func (m *Messenger) Request(ctx context.Context, msg *Message, responseType uint32) (*Message, error) {
	if m == nil || m.vsock == nil {
		return nil, ErrNilTransport
	}
	if msg == nil {
		return nil, ErrNilMessage
	}
	if responseType == 0 {
		return nil, ErrInvalidTypeID
	}

	wait := m.registerPendingResponse(msg.ID, responseType)
	defer m.unregisterPendingResponse(msg.ID, wait)

	if err := m.Send(ctx, msg); err != nil {
		return nil, err
	}

	select {
	case respMsg, ok := <-wait:
		if !ok || respMsg == nil {
			return nil, ErrNilMessage
		}
		return respMsg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
		if err := handler(ctx, msg); err != nil {
			return err
		}
		return m.sendAcknowledge(ctx, msg)
	}
	if err := unknown(ctx, msg); err != nil {
		return err
	}
	return m.sendAcknowledge(ctx, msg)
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

func (m *Messenger) shouldAwaitAcknowledge(msgType uint32) bool {
	return m.config.RequireAcknowledge && msgType != acknowledgeTypeID && msgType != heartbeatTypeID
}

func (m *Messenger) registerPendingAck(id uint64) chan struct{} {
	ch := make(chan struct{})
	m.ackLock.Lock()
	m.pendingAcks[id] = ch
	m.ackLock.Unlock()
	return ch
}

func (m *Messenger) unregisterPendingAck(id uint64, ch chan struct{}) {
	m.ackLock.Lock()
	if current, ok := m.pendingAcks[id]; ok && current == ch {
		delete(m.pendingAcks, id)
	}
	m.ackLock.Unlock()
}

func (m *Messenger) resolveAck(id uint64) {
	m.ackLock.Lock()
	ch, ok := m.pendingAcks[id]
	if ok {
		delete(m.pendingAcks, id)
	}
	m.ackLock.Unlock()
	if ok {
		close(ch)
	}
}

type pendingResponse struct {
	msgType uint32
	ch      chan *Message
}

func (m *Messenger) registerPendingResponse(id uint64, msgType uint32) chan *Message {
	ch := make(chan *Message, 1)
	m.respLock.Lock()
	m.pendingResponses[id] = pendingResponse{msgType: msgType, ch: ch}
	m.respLock.Unlock()
	return ch
}

func (m *Messenger) unregisterPendingResponse(id uint64, ch chan *Message) {
	m.respLock.Lock()
	if current, ok := m.pendingResponses[id]; ok && current.ch == ch {
		delete(m.pendingResponses, id)
	}
	m.respLock.Unlock()
}

func (m *Messenger) resolveResponse(msg *Message) (bool, error) {
	if m == nil || msg == nil {
		return false, nil
	}
	m.respLock.Lock()
	pending, ok := m.pendingResponses[msg.ID]
	if ok && pending.msgType == msg.Type {
		delete(m.pendingResponses, msg.ID)
	}
	m.respLock.Unlock()
	if !ok || pending.msgType != msg.Type {
		return false, nil
	}
	inMemory, err := materializeStreamMessage(msg)
	if err != nil {
		return false, err
	}
	pending.ch <- inMemory
	close(pending.ch)
	return true, nil
}
