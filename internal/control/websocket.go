package control

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type websocketConn struct {
	conn   net.Conn
	reader *bufio.Reader
	client bool
	buf    []byte
}

func DialEndpoint(endpoint Endpoint) (net.Conn, error) {
	return dialWebSocket(endpoint)
}

func dialWebSocket(endpoint Endpoint) (net.Conn, error) {
	parsed, err := url.Parse(endpoint.URL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket URL scheme: %s", parsed.Scheme)
	}
	address := parsed.Host
	if !strings.Contains(address, ":") {
		address += ":80"
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	key, err := websocketKey()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	path := parsed.RequestURI()
	if path == "" {
		path = "/"
	}
	var request bytes.Buffer
	fmt.Fprintf(&request, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&request, "Host: %s\r\n", parsed.Host)
	fmt.Fprintf(&request, "Upgrade: websocket\r\n")
	fmt.Fprintf(&request, "Connection: Upgrade\r\n")
	fmt.Fprintf(&request, "Sec-WebSocket-Key: %s\r\n", key)
	fmt.Fprintf(&request, "Sec-WebSocket-Version: 13\r\n")
	if endpoint.Token != "" {
		fmt.Fprintf(&request, "Authorization: Bearer %s\r\n", endpoint.Token)
	}
	fmt.Fprintf(&request, "\r\n")
	if _, err := conn.Write(request.Bytes()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("websocket upgrade failed: %s", resp.Status)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), websocketAccept(key); got != want {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket upgrade failed: invalid accept key")
	}
	return &websocketConn{conn: conn, reader: reader, client: true}, nil
}

func acceptWebSocket(w http.ResponseWriter, r *http.Request, token string) (net.Conn, error) {
	if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, fmt.Errorf("unauthorized websocket control connection")
	}
	if !headerContains(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return nil, fmt.Errorf("websocket upgrade required")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" || strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		http.Error(w, "bad websocket request", http.StatusBadRequest)
		return nil, fmt.Errorf("invalid websocket request")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return nil, fmt.Errorf("websocket hijacking unsupported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + websocketAccept(key) + "\r\n\r\n"
	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &websocketConn{conn: conn, reader: rw.Reader}, nil
}

func headerContains(header http.Header, name, value string) bool {
	for _, item := range header.Values(name) {
		for _, part := range strings.Split(item, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return true
			}
		}
	}
	return false
}

func websocketKey() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data[:]), nil
}

func websocketAccept(key string) string {
	h := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func (c *websocketConn) Read(p []byte) (int, error) {
	for len(c.buf) == 0 {
		payload, opcode, err := c.readFrame()
		if err != nil {
			return 0, err
		}
		switch opcode {
		case 0x1, 0x2:
			c.buf = payload
		case 0x8:
			return 0, io.EOF
		case 0x9:
			_ = c.writeFrame(0xA, payload)
		}
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *websocketConn) Write(p []byte) (int, error) {
	if err := c.writeFrame(0x1, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *websocketConn) Close() error { return c.conn.Close() }

func (c *websocketConn) LocalAddr() net.Addr { return c.conn.LocalAddr() }

func (c *websocketConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *websocketConn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

func (c *websocketConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

func (c *websocketConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

func (c *websocketConn) readFrame() ([]byte, byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.reader, header[:]); err != nil {
		return nil, 0, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	if length == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	} else if length == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, mask[:]); err != nil {
			return nil, 0, err
		}
	}
	if length > 64*1024*1024 {
		return nil, 0, fmt.Errorf("websocket frame too large")
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, opcode, nil
}

func (c *websocketConn) writeFrame(opcode byte, payload []byte) error {
	var header []byte
	first := byte(0x80) | opcode
	maskBit := byte(0)
	if c.client {
		maskBit = 0x80
	}
	length := len(payload)
	switch {
	case length < 126:
		header = []byte{first, maskBit | byte(length)}
	case length <= 0xffff:
		header = make([]byte, 4)
		header[0] = first
		header[1] = maskBit | 126
		binary.BigEndian.PutUint16(header[2:], uint16(length))
	default:
		header = make([]byte, 10)
		header[0] = first
		header[1] = maskBit | 127
		binary.BigEndian.PutUint64(header[2:], uint64(length))
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if !c.client {
		_, err := c.conn.Write(payload)
		return err
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	if _, err := c.conn.Write(mask[:]); err != nil {
		return err
	}
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := c.conn.Write(masked)
	return err
}
