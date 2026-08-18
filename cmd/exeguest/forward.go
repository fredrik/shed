//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"

	"github.com/mdlayher/vsock"

	"github.com/fredrik/local-devexe/internal/vsockproto"
)

// startForwarder accepts host connections on the well-known vsock forward
// port and splices each one to a TCP port on the guest loopback. It gives
// the host a TCC-proof way to reach any service in the VM.
func startForwarder() {
	ln, err := vsock.Listen(vsockproto.ForwardPort, nil)
	if err != nil {
		fmt.Printf("exeguest: forwarder unavailable: %v\n", err)
		return
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				fmt.Printf("exeguest: forwarder accept: %v\n", err)
				return
			}
			go forwardConn(conn)
		}
	}()
}

func forwardConn(conn net.Conn) {
	defer conn.Close()
	var hdr vsockproto.ForwardHeader
	dec := json.NewDecoder(conn)
	if err := dec.Decode(&hdr); err != nil {
		return
	}
	target, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", hdr.Port))
	if err != nil {
		return
	}
	defer target.Close()
	// The JSON decoder may have buffered bytes past the header; replay
	// them before splicing.
	go func() {
		io.Copy(target, io.MultiReader(dec.Buffered(), conn))
		if tc, ok := target.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
	}()
	io.Copy(conn, target)
}
