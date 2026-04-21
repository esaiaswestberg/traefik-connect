package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"example.com/traefik-connect/internal/websocketx"
)

const (
	MsgRequestStart  = "request_start"
	MsgRequestEnd    = "request_end"
	MsgResponseStart = "response_start"
)

type RequestStart struct {
	Type          string      `json:"type"`
	Method        string      `json:"method"`
	Path          string      `json:"path"`
	RawQuery      string      `json:"raw_query,omitempty"`
	Host          string      `json:"host,omitempty"`
	Header        http.Header `json:"header,omitempty"`
	ContentLength int64       `json:"content_length,omitempty"`
}

type ResponseStart struct {
	Type   string      `json:"type"`
	Status int         `json:"status"`
	Header http.Header `json:"header,omitempty"`
}

type Stream struct {
	Conn *websocketx.Conn
}

func Dial(ctx context.Context, targetURL string, headers http.Header) (*Stream, error) {
	conn, err := websocketx.Dial(ctx, targetURL, headers)
	if err != nil {
		return nil, err
	}
	return &Stream{Conn: conn}, nil
}

func Accept(w http.ResponseWriter, r *http.Request) (*Stream, error) {
	conn, err := websocketx.Accept(w, r)
	if err != nil {
		return nil, err
	}
	return &Stream{Conn: conn}, nil
}

func (s *Stream) Close() error { return s.Conn.Close() }

func (s *Stream) WriteRequestStart(msg RequestStart) error {
	msg.Type = MsgRequestStart
	return s.writeJSON(msg)
}

func (s *Stream) WriteRequestEnd() error {
	return s.writeJSON(map[string]string{"type": MsgRequestEnd})
}

func (s *Stream) WriteResponseStart(msg ResponseStart) error {
	msg.Type = MsgResponseStart
	return s.writeJSON(msg)
}

func (s *Stream) ReadRequestStart() (RequestStart, error) {
	var msg RequestStart
	if err := s.readJSON(&msg); err != nil {
		return RequestStart{}, err
	}
	if msg.Type != MsgRequestStart {
		return RequestStart{}, fmt.Errorf("unexpected message %q", msg.Type)
	}
	return msg, nil
}

func (s *Stream) ReadResponseStart() (ResponseStart, error) {
	var msg ResponseStart
	if err := s.readJSON(&msg); err != nil {
		return ResponseStart{}, err
	}
	if msg.Type != MsgResponseStart {
		return ResponseStart{}, fmt.Errorf("unexpected message %q", msg.Type)
	}
	return msg, nil
}

func (s *Stream) ReadMessage() (opcode byte, payload []byte, err error) {
	return s.Conn.ReadFrame()
}

func (s *Stream) WriteBinary(payload []byte) error { return s.Conn.WriteBinary(payload) }
func (s *Stream) WriteText(payload []byte) error   { return s.Conn.WriteText(payload) }
func (s *Stream) WriteClose(payload []byte) error  { return s.Conn.WriteClose(payload) }
func (s *Stream) Flush() error                     { return s.Conn.Flush() }

func (s *Stream) readJSON(v any) error {
	op, payload, err := s.Conn.ReadFrame()
	if err != nil {
		return err
	}
	if op != 0x1 {
		return fmt.Errorf("unexpected opcode %d", op)
	}
	return json.Unmarshal(payload, v)
}

func (s *Stream) writeJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Conn.WriteText(raw)
}
