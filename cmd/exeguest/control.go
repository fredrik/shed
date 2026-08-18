//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/mdlayher/vsock"

	"github.com/fredrik/local-devexe/internal/vsockproto"
)

type controlConn struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// dialControl connects to the host daemon's vsock listener, retrying while
// the host wires up its side of the freshly started VM.
func dialControl() (*controlConn, error) {
	var conn net.Conn
	var err error
	for attempt := 0; attempt < 40; attempt++ {
		conn, err = vsock.Dial(vsockproto.HostCID, vsockproto.Port, nil)
		if err == nil {
			return &controlConn{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, fmt.Errorf("vsock dial: %w", err)
}

// hello asks the host for this VM's configuration.
func (c *controlConn) hello() (*vsockproto.Config, error) {
	if err := c.enc.Encode(vsockproto.Message{Type: vsockproto.TypeHello}); err != nil {
		return nil, fmt.Errorf("send hello: %w", err)
	}
	var msg vsockproto.Message
	if err := c.dec.Decode(&msg); err != nil {
		return nil, fmt.Errorf("await config: %w", err)
	}
	if msg.Type != vsockproto.TypeConfig || msg.Config == nil {
		return nil, fmt.Errorf("expected config, got %q", msg.Type)
	}
	return msg.Config, nil
}

func (c *controlConn) ready(ip, hostname string) error {
	return c.enc.Encode(vsockproto.Message{Type: vsockproto.TypeReady, IP: ip, Hostname: hostname})
}

func (c *controlConn) reportError(err error) {
	c.enc.Encode(vsockproto.Message{Type: vsockproto.TypeError, Error: err.Error()})
}

// waitShutdown serves the control loop until the host requests shutdown or
// the channel breaks (host daemon died → power off, the host will reconcile).
func (c *controlConn) waitShutdown() error {
	for {
		var msg vsockproto.Message
		if err := c.dec.Decode(&msg); err != nil {
			fmt.Printf("exeguest: control channel lost (%v), powering off\n", err)
			return nil
		}
		switch msg.Type {
		case vsockproto.TypeShutdown:
			fmt.Println("exeguest: shutdown requested by host")
			return nil
		default:
			fmt.Printf("exeguest: unknown control message %q\n", msg.Type)
		}
	}
}
