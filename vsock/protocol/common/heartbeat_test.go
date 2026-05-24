package common

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/hlfshell/poprocks/vsock"
)

func TestHeartbeatConfigValidate(t *testing.T) {
	if err := (&HeartbeatConfig{Interval: 2 * time.Second, Timeout: time.Second}).Validate(); err == nil {
		t.Fatal("expected heartbeat timeout validation error")
	}
	if err := (&HeartbeatConfig{Interval: 100 * time.Millisecond, Timeout: 300 * time.Millisecond}).Validate(); err != nil {
		t.Fatalf("unexpected heartbeat validation error: %v", err)
	}
}

func TestHeartbeatRequestResponseEndToEnd(t *testing.T) {
	hostConn, clientConn := net.Pipe()
	defer hostConn.Close()
	defer clientConn.Close()

	host := vsock.NewMessenger(hostConn)
	client := vsock.NewMessenger(clientConn)

	hostHeartbeat, err := NewHeartbeat(host, HeartbeatConfig{
		Interval: 100 * time.Millisecond,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("new host heartbeat: %v", err)
	}
	clientHeartbeat, err := NewHeartbeat(client, HeartbeatConfig{
		Interval: 100 * time.Millisecond,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("new client heartbeat: %v", err)
	}
	clientHeartbeat.OnHealth(func() map[string]any { return map[string]any{"side": "client"} })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostErr := make(chan error, 1)
	clientErr := make(chan error, 1)
	go func() { hostErr <- host.Serve(ctx) }()
	go func() { clientErr <- client.Serve(ctx) }()

	if err := hostHeartbeat.ping(ctx); err != nil {
		t.Fatalf("heartbeat ping: %v", err)
	}
	state := hostHeartbeat.State()
	if state.Status != HeartbeatStatusOK {
		t.Fatalf("unexpected heartbeat status: %d", state.Status)
	}
	if state.Health == nil || state.Health["side"] != "client" {
		t.Fatalf("unexpected heartbeat health: %#v", state.Health)
	}

	cancel()
	if err := <-hostErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("host serve error: %v", err)
	}
	if err := <-clientErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("client serve error: %v", err)
	}
}

func TestHeartbeatDuplicateRegistration(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	m := vsock.NewMessenger(c1)
	if _, err := NewHeartbeat(m, DefaultHeartbeatConfig()); err != nil {
		t.Fatalf("new heartbeat: %v", err)
	}
	if _, err := NewHeartbeat(m, DefaultHeartbeatConfig()); !errors.Is(err, vsock.ErrHandlerAlreadyRegistered) {
		t.Fatalf("expected duplicate registration error, got %v", err)
	}
}
