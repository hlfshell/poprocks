package vsock

import (
	"context"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

type Heartbeat struct {
	Sent     time.Time
	Received time.Time

	Status uint8
	Health map[string]any
}

// Heartbeat Statuses
const (
	HeartbeatStatusUnknown = iota
	HeartbeatStatusOK
	HeartbeatStatusWarning
	HeartbeatStatusError
	HeartbeatStatusStarting
	HeartbeatStatusStopping
)

const heartbeatTypeID uint32 = 0xFFFFFFFE

type heartbeatPayload struct {
	SentAt time.Time      `msgpack:"sent_at"`
	Status uint8          `msgpack:"status"`
	Health map[string]any `msgpack:"health,omitempty"`
}

func (m *Messenger) StartHeartbeat(ctx context.Context) {
	if m == nil || !m.config.Heartbeat || !m.config.HeartbeatHost {
		return
	}

	m.hbLock.Lock()
	if m.heartbeatStarted {
		m.hbLock.Unlock()
		return
	}
	m.heartbeatStarted = true
	m.hbLock.Unlock()

	go m.heartbeatLoop(ctx)
}

func (m *Messenger) heartbeatLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			m.stopHeartbeat()
			return
		default:
		}

		m.setHeartbeatStatus(HeartbeatStatusStarting)

		waitCh, err := m.heartbeatPing(ctx)
		if err != nil {
			m.setHeartbeatStatus(HeartbeatStatusError)
		} else {
			select {
			case <-waitCh:
				m.hbLock.Lock()
				m.heartbeat.Received = time.Now()
				m.heartbeat.Status = HeartbeatStatusOK
				m.hbLock.Unlock()
			case <-time.After(m.heartbeatTimeout()):
				m.setHeartbeatStatus(HeartbeatStatusWarning)
			case <-ctx.Done():
				m.stopHeartbeat()
				return
			}
		}

		select {
		case <-time.After(m.heartbeatInterval()):
		case <-ctx.Done():
			m.stopHeartbeat()
			return
		}
	}
}

func (m *Messenger) heartbeatPing(ctx context.Context) (<-chan struct{}, error) {
	payload := heartbeatPayload{
		SentAt: time.Now(),
		Status: HeartbeatStatusOK,
		Health: m.snapshotHealth(),
	}
	msg, err := m.newHeartbeatMessage(payload)
	if err != nil {
		return nil, err
	}

	done := make(chan struct{})
	m.hbLock.Lock()
	m.heartbeatPending = done
	m.heartbeat.Sent = payload.SentAt
	m.heartbeat.Health = payload.Health
	m.hbLock.Unlock()

	if err := m.Send(ctx, msg); err != nil {
		m.hbLock.Lock()
		m.heartbeatPending = nil
		m.hbLock.Unlock()
		close(done)
		return nil, err
	}
	return done, nil
}

func (m *Messenger) handleHeartbeatMessage(ctx context.Context, msg *Message) error {
	var p heartbeatPayload
	if err := msgpack.Unmarshal(msg.Payload, &p); err != nil {
		return err
	}

	if !m.config.HeartbeatHost {
		response := heartbeatPayload{
			SentAt: p.SentAt,
			Status: HeartbeatStatusOK,
			Health: m.snapshotHealth(),
		}
		out, err := m.newHeartbeatMessage(response)
		if err != nil {
			return err
		}
		return m.Send(ctx, out)
	}

	// Host mode treats incoming heartbeat as response to its probe.
	m.hbLock.Lock()
	if m.heartbeatPending != nil {
		ch := m.heartbeatPending
		m.heartbeatPending = nil
		close(ch)
	}
	m.heartbeat.Received = time.Now()
	m.heartbeat.Status = p.Status
	m.heartbeat.Health = p.Health
	m.hbLock.Unlock()
	return nil
}

func (m *Messenger) setHeartbeatStatus(status uint8) {
	m.hbLock.Lock()
	m.heartbeat.Status = status
	m.hbLock.Unlock()
}

func (m *Messenger) stopHeartbeat() {
	m.hbLock.Lock()
	m.heartbeat.Status = HeartbeatStatusStopping
	m.heartbeatStarted = false
	m.hbLock.Unlock()
}

func (m *Messenger) heartbeatTimeout() time.Duration {
	if m.config.HeartbeatTimeout > 0 {
		return m.config.HeartbeatTimeout
	}
	return DefaultHeartbeatTimeout
}

func (m *Messenger) heartbeatInterval() time.Duration {
	if m.config.HeartbeatInterval > 0 {
		return m.config.HeartbeatInterval
	}
	return DefaultHeartbeatInterval
}

func (m *Messenger) newHeartbeatMessage(payload heartbeatPayload) (*Message, error) {
	body, err := msgpack.Marshal(payload)
	if err != nil {
		return nil, err
	}
	id := uint64(time.Now().UnixNano())
	if id == 0 {
		id = 1
	}
	return NewMessage(id, heartbeatTypeID, body), nil
}

func (m *Messenger) snapshotHealth() map[string]any {
	m.hbLock.Lock()
	defer m.hbLock.Unlock()

	if m.heartbeatHealth == nil {
		return nil
	}
	return m.heartbeatHealth()
}

func (m *Messenger) HeartbeatState() Heartbeat {
	m.hbLock.Lock()
	defer m.hbLock.Unlock()
	out := m.heartbeat
	if out.Health != nil {
		copied := make(map[string]any, len(out.Health))
		for k, v := range out.Health {
			copied[k] = v
		}
		out.Health = copied
	}
	return out
}
