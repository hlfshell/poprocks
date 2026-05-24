package common

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hlfshell/poprocks/vsock"
	"github.com/hlfshell/poprocks/vsock/protocol"
)

const (
	TypeHeartbeat uint32 = 0xFFFF1001
)

const (
	HeartbeatStatusUnknown = iota
	HeartbeatStatusOK
	HeartbeatStatusWarning
	HeartbeatStatusError
	HeartbeatStatusStarting
	HeartbeatStatusStopping
)

const (
	DefaultHeartbeatInterval = 15 * time.Second
	DefaultHeartbeatTimeout  = 45 * time.Second
)

type HeartbeatConfig struct {
	Interval time.Duration
	Timeout  time.Duration
}

type HeartbeatState struct {
	Sent     time.Time
	Received time.Time

	Status uint8
	Health map[string]any
}

type HeartbeatPayload struct {
	SentAt time.Time      `msgpack:"sent_at"`
	Status uint8          `msgpack:"status"`
	Health map[string]any `msgpack:"health,omitempty"`
}

type Heartbeat struct {
	transport *protocol.R[HeartbeatPayload, HeartbeatPayload]
	lock      sync.Mutex
	config    HeartbeatConfig

	state   HeartbeatState
	started bool
	health  func() map[string]any
}

func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		Interval: DefaultHeartbeatInterval,
		Timeout:  DefaultHeartbeatTimeout,
	}
}

func (c *HeartbeatConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.Interval <= 0 {
		return fmt.Errorf("heartbeat interval must be > 0")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("heartbeat timeout must be > 0")
	}
	if c.Timeout <= c.Interval {
		return fmt.Errorf("heartbeat timeout must be greater than heartbeat interval")
	}
	return nil
}

func NewHeartbeat(messenger *vsock.Messenger, cfg HeartbeatConfig) (*Heartbeat, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	reqCodec, err := vsock.NewCodecOfType[HeartbeatPayload](TypeHeartbeat, vsock.CodecMsgpack)
	if err != nil {
		return nil, err
	}
	respCodec, err := vsock.NewCodecOfType[HeartbeatPayload](TypeHeartbeat+100, vsock.CodecMsgpack)
	if err != nil {
		return nil, err
	}
	transport, err := protocol.NewR[HeartbeatPayload, HeartbeatPayload](messenger, reqCodec, respCodec)
	if err != nil {
		return nil, err
	}
	h := &Heartbeat{
		transport: transport,
		config:    cfg,
	}
	if err := transport.OnRequest(h.handleRequest); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *Heartbeat) Start(ctx context.Context) {
	if h == nil {
		return
	}
	h.lock.Lock()
	if h.started {
		h.lock.Unlock()
		return
	}
	h.started = true
	h.lock.Unlock()

	go h.loop(ctx)
}

func (h *Heartbeat) OnHealth(provider func() map[string]any) {
	h.lock.Lock()
	h.health = provider
	h.lock.Unlock()
}

func (h *Heartbeat) State() HeartbeatState {
	h.lock.Lock()
	defer h.lock.Unlock()
	out := h.state
	if out.Health != nil {
		copied := make(map[string]any, len(out.Health))
		for k, v := range out.Health {
			copied[k] = v
		}
		out.Health = copied
	}
	return out
}

func (h *Heartbeat) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.stop()
			return
		default:
		}

		h.setStatus(HeartbeatStatusStarting)
		if err := h.ping(ctx); err != nil {
			h.setStatus(HeartbeatStatusError)
		}

		select {
		case <-time.After(h.interval()):
		case <-ctx.Done():
			h.stop()
			return
		}
	}
}

func (h *Heartbeat) ping(ctx context.Context) error {
	payload := HeartbeatPayload{
		SentAt: time.Now(),
		Status: HeartbeatStatusOK,
		Health: h.snapshotHealth(),
	}
	h.lock.Lock()
	h.state.Sent = payload.SentAt
	h.state.Health = payload.Health
	h.lock.Unlock()

	reqCtx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()
	resp, err := h.transport.Request(reqCtx, payload)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			h.setStatus(HeartbeatStatusWarning)
			return nil
		}
		h.setStatus(HeartbeatStatusWarning)
		return err
	}

	h.lock.Lock()
	h.state.Received = time.Now()
	h.state.Status = resp.Status
	h.state.Health = resp.Health
	h.lock.Unlock()
	return nil
}

func (h *Heartbeat) handleRequest(ctx context.Context, req HeartbeatPayload) (HeartbeatPayload, error) {
	_ = ctx
	resp := HeartbeatPayload{
		SentAt: req.SentAt,
		Status: HeartbeatStatusOK,
		Health: h.snapshotHealth(),
	}
	h.lock.Lock()
	h.state.Received = time.Now()
	h.state.Status = req.Status
	h.state.Health = req.Health
	h.lock.Unlock()
	return resp, nil
}

func (h *Heartbeat) snapshotHealth() map[string]any {
	h.lock.Lock()
	provider := h.health
	h.lock.Unlock()

	if provider == nil {
		return nil
	}
	return provider()
}

func (h *Heartbeat) setStatus(status uint8) {
	h.lock.Lock()
	h.state.Status = status
	h.lock.Unlock()
}

func (h *Heartbeat) stop() {
	h.lock.Lock()
	h.state.Status = HeartbeatStatusStopping
	h.started = false
	h.lock.Unlock()
}

func (h *Heartbeat) timeout() time.Duration {
	return h.config.Timeout
}

func (h *Heartbeat) interval() time.Duration {
	return h.config.Interval
}
