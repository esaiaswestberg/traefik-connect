package websocketx

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

type Conn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	mu   sync.Mutex
	mask bool
}

func Dial(ctx context.Context, rawURL string, extra http.Header) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	addr := u.Host
	if !strings.Contains(addr, ":") {
		if u.Scheme == "https" || u.Scheme == "wss" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if _, err := writeClientHandshake(conn, u, extra); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	req := &http.Request{Method: http.MethodGet}
	res, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake failed: %s", res.Status)
	}
	accept := res.Header.Get("Sec-WebSocket-Accept")
	if accept == "" {
		conn.Close()
		return nil, fmt.Errorf("websocket handshake missing accept header")
	}
	return &Conn{conn: conn, r: br, w: bufio.NewWriter(conn), mask: true}, nil
}

func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("hijacking unsupported")
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	if !websocketUpgrade(r) {
		conn.Close()
		return nil, fmt.Errorf("upgrade required")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		conn.Close()
		return nil, fmt.Errorf("missing websocket key")
	}
	accept := websocketAccept(key)
	if _, err := fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(buf, "Upgrade: websocket\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(buf, "Connection: Upgrade\r\n"); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(buf, "Sec-WebSocket-Accept: %s\r\n\r\n", accept); err != nil {
		conn.Close()
		return nil, err
	}
	if err := buf.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn), mask: false}, nil
}

func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) ReadFrame() (opcode byte, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err = io.ReadFull(c.r, header); err != nil {
		return 0, nil, err
	}
	opcode = header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(c.r, ext); err != nil {
			return 0, nil, err
		}
		length = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(c.r, ext); err != nil {
			return 0, nil, err
		}
		length = 0
		for _, b := range ext {
			length = (length << 8) | int64(b)
		}
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

func (c *Conn) WriteText(payload []byte) error   { return c.writeFrame(0x1, payload) }
func (c *Conn) WriteBinary(payload []byte) error { return c.writeFrame(0x2, payload) }
func (c *Conn) WriteClose(payload []byte) error  { return c.writeFrame(0x8, payload) }

func (c *Conn) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.Flush()
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b0 := byte(0x80 | (opcode & 0x0F))
	if err := c.w.WriteByte(b0); err != nil {
		return err
	}
	if c.mask {
		switch {
		case len(payload) < 126:
			if err := c.w.WriteByte(byte(0x80 | len(payload))); err != nil {
				return err
			}
		case len(payload) <= 0xFFFF:
			if err := c.w.WriteByte(0x80 | 126); err != nil {
				return err
			}
			if err := c.w.WriteByte(byte(len(payload) >> 8)); err != nil {
				return err
			}
			if err := c.w.WriteByte(byte(len(payload))); err != nil {
				return err
			}
		default:
			if err := c.w.WriteByte(0x80 | 127); err != nil {
				return err
			}
			for shift := 56; shift >= 0; shift -= 8 {
				if err := c.w.WriteByte(byte(uint64(len(payload)) >> shift)); err != nil {
					return err
				}
			}
		}
		var maskKey [4]byte
		if _, err := rand.Read(maskKey[:]); err != nil {
			return err
		}
		if _, err := c.w.Write(maskKey[:]); err != nil {
			return err
		}
		masked := make([]byte, len(payload))
		for i := range payload {
			masked[i] = payload[i] ^ maskKey[i%4]
		}
		if _, err := c.w.Write(masked); err != nil {
			return err
		}
		return c.w.Flush()
	}
	switch {
	case len(payload) < 126:
		if err := c.w.WriteByte(byte(len(payload))); err != nil {
			return err
		}
	case len(payload) <= 0xFFFF:
		if err := c.w.WriteByte(126); err != nil {
			return err
		}
		if err := c.w.WriteByte(byte(len(payload) >> 8)); err != nil {
			return err
		}
		if err := c.w.WriteByte(byte(len(payload))); err != nil {
			return err
		}
	default:
		if err := c.w.WriteByte(127); err != nil {
			return err
		}
		for shift := 56; shift >= 0; shift -= 8 {
			if err := c.w.WriteByte(byte(uint64(len(payload)) >> shift)); err != nil {
				return err
			}
		}
	}
	if _, err := c.w.Write(payload); err != nil {
		return err
	}
	return c.w.Flush()
}

func writeClientHandshake(conn net.Conn, u *url.URL, extra http.Header) (string, error) {
	key := websocketKey()
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	host := u.Host
	if host == "" {
		host = "localhost"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", path)
	fmt.Fprintf(&b, "Host: %s\r\n", host)
	fmt.Fprintf(&b, "Upgrade: websocket\r\n")
	fmt.Fprintf(&b, "Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Version: 13\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	for k, vals := range extra {
		for _, v := range vals {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return "", err
	}
	return key, nil
}

func websocketKey() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return base64.StdEncoding.EncodeToString(raw[:])
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func websocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
