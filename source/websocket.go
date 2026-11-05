package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// dialWebsocket opens a binary websocket to Proxmox. It is a small RFC 6455
// client because the plugin build adds no modules beyond the SDK.
func dialWebsocket(ctx context.Context, p *pveClient, path string, query url.Values) (*wsConn, error) {
	base, err := url.Parse(p.base)
	if err != nil {
		return nil, fmt.Errorf("parse Proxmox endpoint: %w", err)
	}
	port := base.Port()
	if port == "" {
		port = "80"
		if base.Scheme == "https" {
			port = "443"
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(base.Hostname(), port))
	if err != nil {
		return nil, fmt.Errorf("dial Proxmox: %w", err)
	}
	conn := raw
	if base.Scheme == "https" {
		config := p.tlsConfig.Clone()
		config.ServerName = base.Hostname()
		client := tls.Client(raw, config)
		if err := client.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("Proxmox TLS handshake: %w", err)
		}
		conn = client
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			conn.Close()
			return nil, err
		}
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce)
	target := base.EscapedPath() + path + "?" + query.Encode()
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\n"+
		"Authorization: %s\r\n\r\n", target, base.Host, key, p.auth)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("send websocket upgrade: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read websocket upgrade: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		resp.Body.Close()
		conn.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%w: websocket returned HTTP %d", errConsoleDenied, resp.StatusCode)
		}
		return nil, fmt.Errorf("Proxmox websocket returned HTTP %d", resp.StatusCode)
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != websocketAccept(key) {
		conn.Close()
		return nil, errors.New("Proxmox websocket returned a bad accept key")
	}
	return &wsConn{conn: conn, reader: reader}, nil
}

// wsConn presents a websocket's binary payload as a byte stream. It is used
// from one goroutine at a time.
type wsConn struct {
	conn      net.Conn
	reader    *bufio.Reader
	remaining int64
}

func (w *wsConn) SetReadDeadline(t time.Time) error { return w.conn.SetReadDeadline(t) }

func (w *wsConn) Read(p []byte) (int, error) {
	for w.remaining == 0 {
		if err := w.nextFrame(); err != nil {
			return 0, err
		}
	}
	if int64(len(p)) > w.remaining {
		p = p[:w.remaining]
	}
	n, err := w.reader.Read(p)
	w.remaining -= int64(n)
	return n, err
}

// nextFrame reads one frame header. Control frames are handled here and leave
// remaining at 0, so Read simply moves on to the next frame.
func (w *wsConn) nextFrame() error {
	var header [2]byte
	if _, err := io.ReadFull(w.reader, header[:]); err != nil {
		return err
	}
	opcode := header[0] & 0x0f
	if header[1]&0x80 != 0 {
		return errors.New("websocket server sent a masked frame")
	}
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(w.reader, extended[:]); err != nil {
			return err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(w.reader, extended[:]); err != nil {
			return err
		}
		value := binary.BigEndian.Uint64(extended[:])
		if value > 1<<31 {
			return errors.New("websocket frame is too large")
		}
		length = int64(value)
	}
	switch opcode {
	case 0x0, 0x1, 0x2: // continuation, text, binary
		w.remaining = length
		return nil
	case 0x8: // close
		return io.EOF
	case 0x9, 0xA: // ping, pong
		if length > 125 {
			return errors.New("websocket control frame is too large")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(w.reader, payload); err != nil {
			return err
		}
		if opcode == 0x9 {
			return w.writeFrame(0xA, payload)
		}
		return nil
	default:
		return fmt.Errorf("unknown websocket opcode %d", opcode)
	}
}

func (w *wsConn) Write(p []byte) (int, error) {
	if err := w.writeFrame(0x2, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// writeFrame writes one masked frame, as RFC 6455 requires of clients.
func (w *wsConn) writeFrame(opcode byte, payload []byte) error {
	frame := make([]byte, 0, len(payload)+14)
	frame = append(frame, 0x80|opcode)
	switch {
	case len(payload) <= 125:
		frame = append(frame, 0x80|byte(len(payload)))
	case len(payload) <= 0xffff:
		frame = append(frame, 0x80|126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
	default:
		frame = append(frame, 0x80|127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(payload)))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, err := w.conn.Write(frame)
	return err
}

func (w *wsConn) Close() error {
	_ = w.writeFrame(0x8, nil)
	return w.conn.Close()
}
