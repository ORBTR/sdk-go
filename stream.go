package orbtr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Server-sent event streams. A Messages stream yields a MessageStreamEvent per event: for the Anthropic
// event types the matching field is set, and any other event name (ORBTR's orbtr.ai.* progress events)
// arrives with only Type and its RawJSON; an error event ends the stream with a *StreamError. An
// OpenAI-compatible stream yields each data line's JSON, decoded, until data: [DONE].

// ServerSentEvent is one decoded server-sent event.
type ServerSentEvent struct {
	Event string
	Data  []byte
	ID    string
}

// SSEDecoder splits a byte stream into server-sent events, following the event-stream format: lines end in
// LF, CR or CRLF; a blank line ends an event; a line starting with a colon is a comment; data lines join
// with LF.
type SSEDecoder struct {
	scanner *bufio.Scanner
	event   string
	data    [][]byte
	hasData bool
	id      string
}

// NewSSEDecoder reads events from r.
func NewSSEDecoder(r io.Reader) *SSEDecoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	s.Split(scanEventLines)
	return &SSEDecoder{scanner: s}
}

func scanEventLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\n' {
			return i + 1, data[:i], nil
		}
		if b == '\r' {
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			// A CR at the buffer's end may be the first half of a CRLF; read more first.
			return 0, nil, nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// Next returns the next event, or io.EOF when the stream ends.
func (d *SSEDecoder) Next() (ServerSentEvent, error) {
	for d.scanner.Scan() {
		if ev, ok := d.line(d.scanner.Bytes()); ok {
			return ev, nil
		}
	}
	if err := d.scanner.Err(); err != nil {
		return ServerSentEvent{}, err
	}
	if ev, ok := d.line(nil); ok {
		return ev, nil
	}
	return ServerSentEvent{}, io.EOF
}

func (d *SSEDecoder) line(line []byte) (ServerSentEvent, bool) {
	if len(line) == 0 {
		if !d.hasData && d.event == "" {
			return ServerSentEvent{}, false
		}
		ev := ServerSentEvent{Event: d.event, Data: bytes.Join(d.data, []byte("\n")), ID: d.id}
		d.event, d.data, d.hasData = "", nil, false
		return ev, true
	}
	if line[0] == ':' {
		return ServerSentEvent{}, false
	}
	field, value, found := bytes.Cut(line, []byte(":"))
	if found && len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(field) {
	case "event":
		d.event = string(value)
	case "data":
		d.data = append(d.data, append([]byte(nil), value...))
		d.hasData = true
	case "id":
		d.id = string(value)
	}
	return ServerSentEvent{}, false
}

// errSkip and errDone tell a stream to pass over an event or to end.
var (
	errSkip = errors.New("skip")
	errDone = errors.New("done")
)

// Stream is a streamed reply. Call Next until it returns false, reading each value with Current; then Err
// reports why it stopped (nil at the stream's end). Close ends it early.
type Stream[T any] struct {
	resp    *http.Response
	cancel  context.CancelFunc
	decoder *SSEDecoder
	parse   func(ServerSentEvent) (T, error)
	cur     T
	err     error
	done    bool
}

func newStream[T any](resp *http.Response, cancel context.CancelFunc, parse func(ServerSentEvent) (T, error)) *Stream[T] {
	return &Stream[T]{resp: resp, cancel: cancel, decoder: NewSSEDecoder(resp.Body), parse: parse}
}

// Response is the HTTP response the stream reads.
func (s *Stream[T]) Response() *http.Response { return s.resp }

// Next reads the next value; it returns false at the stream's end or on an error.
func (s *Stream[T]) Next() bool {
	for !s.done {
		ev, err := s.decoder.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.err = &ConnectionError{Err: err}
			}
			s.finish()
			return false
		}
		v, err := s.parse(ev)
		if errors.Is(err, errSkip) {
			continue
		}
		if err != nil {
			if !errors.Is(err, errDone) {
				s.err = err
			}
			s.finish()
			return false
		}
		s.cur = v
		return true
	}
	return false
}

// Current is the value Next read.
func (s *Stream[T]) Current() T { return s.cur }

// Err is the error that ended the stream, or nil.
func (s *Stream[T]) Err() error { return s.err }

// Close ends the stream and its connection.
func (s *Stream[T]) Close() error {
	s.finish()
	return nil
}

func (s *Stream[T]) finish() {
	if s.done {
		return
	}
	s.done = true
	s.resp.Body.Close()
	s.cancel()
}

func streamError(data []byte) *StreamError {
	var body struct {
		Error struct {
			Type    string  `json:"type"`
			Code    *string `json:"code"`
			Message string  `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	code := body.Error.Type
	if body.Error.Code != nil && *body.Error.Code != "" {
		code = *body.Error.Code
	}
	msg := body.Error.Message
	if msg == "" {
		msg = "the stream failed"
	}
	return &StreamError{Code: code, Message: msg, Body: append([]byte(nil), data...)}
}

func chunkStream[T any](ctx context.Context, c *Client, req request, opts []RequestOption) (*Stream[T], error) {
	resp, cancel, err := c.send(ctx, req, opts, "text/event-stream", true)
	if err != nil {
		return nil, err
	}
	return newStream(resp, cancel, func(ev ServerSentEvent) (T, error) {
		var v T
		data := bytes.TrimSpace(ev.Data)
		if string(data) == "[DONE]" {
			return v, errDone
		}
		if len(data) == 0 {
			return v, errSkip
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(data, &probe) == nil && probe["error"] != nil && probe["choices"] == nil {
			return v, streamError(data)
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return v, &StreamError{Message: "an event is not valid JSON: " + err.Error(), Body: data}
		}
		return v, nil
	}), nil
}

var messageEventTypes = map[string]bool{
	"message_start": true, "content_block_start": true, "content_block_delta": true, "content_block_stop": true,
	"message_delta": true, "message_stop": true, "ping": true,
}

func parseMessageEvent(ev ServerSentEvent) (MessageStreamEvent, error) {
	name := ev.Event
	if name == "" {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(ev.Data, &head)
		name = head.Type
	}
	if name == "error" {
		return MessageStreamEvent{}, streamError(ev.Data)
	}
	if name == "" {
		return MessageStreamEvent{}, errSkip
	}
	if messageEventTypes[name] {
		var e MessageStreamEvent
		if err := json.Unmarshal(ev.Data, &e); err != nil {
			return e, &StreamError{Message: "an event is not valid JSON: " + err.Error(), Body: ev.Data}
		}
		e.Type = name
		return e, nil
	}
	return MessageStreamEvent{Type: name, raw: append(json.RawMessage(nil), ev.Data...)}, nil
}

// MessageStream is a streamed Messages reply. Besides its events, it gathers the reply: FinalMessage returns
// the Message the events describe once the stream ends.
type MessageStream struct {
	*Stream[MessageStreamEvent]
	message *Message
	partial map[int64][]byte
}

func (c *Client) messageStream(ctx context.Context, req request, opts []RequestOption) (*MessageStream, error) {
	resp, cancel, err := c.send(ctx, req, opts, "text/event-stream", true)
	if err != nil {
		return nil, err
	}
	return &MessageStream{Stream: newStream(resp, cancel, parseMessageEvent), partial: map[int64][]byte{}}, nil
}

// Next reads the next event and gathers it into the reply.
func (s *MessageStream) Next() bool {
	if !s.Stream.Next() {
		return false
	}
	s.observe(s.Current())
	return true
}

// CurrentMessage is the reply as gathered so far, or nil before message_start.
func (s *MessageStream) CurrentMessage() *Message { return s.message }

// FinalMessage reads the rest of the stream and returns the whole reply.
func (s *MessageStream) FinalMessage() (*Message, error) {
	for s.Next() {
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if s.message == nil {
		return nil, &StreamError{Message: "the stream ended before message_start"}
	}
	return s.message, nil
}

// FinalText reads the rest of the stream and returns the reply's text.
func (s *MessageStream) FinalText() (string, error) {
	m, err := s.FinalMessage()
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	for _, blk := range m.Content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String(), nil
}

func (s *MessageStream) observe(ev MessageStreamEvent) {
	switch {
	case ev.MessageStart != nil:
		m := ev.MessageStart.Message
		m.Content = append([]ContentBlock(nil), m.Content...)
		s.message = &m
	case ev.ContentBlockStart != nil && s.message != nil:
		i := ev.ContentBlockStart.Index
		for int64(len(s.message.Content)) <= i {
			s.message.Content = append(s.message.Content, ContentBlock{})
		}
		s.message.Content[i] = ev.ContentBlockStart.ContentBlock
	case ev.ContentBlockDelta != nil && s.message != nil:
		d := ev.ContentBlockDelta
		if d.Index >= int64(len(s.message.Content)) {
			return
		}
		switch d.Delta.Type {
		case "text_delta":
			s.message.Content[d.Index].Text += d.Delta.Text
		case "input_json_delta":
			s.partial[d.Index] = append(s.partial[d.Index], d.Delta.PartialJSON...)
		}
	case ev.ContentBlockStop != nil && s.message != nil:
		i := ev.ContentBlockStop.Index
		if js := s.partial[i]; len(js) > 0 && i < int64(len(s.message.Content)) {
			var input map[string]any
			if json.Unmarshal(js, &input) == nil {
				s.message.Content[i].Input = input
			}
		}
		delete(s.partial, i)
	case ev.MessageDelta != nil && s.message != nil:
		s.message.StopReason = ev.MessageDelta.Delta.StopReason
		s.message.StopSequence = ev.MessageDelta.Delta.StopSequence
		if u := ev.MessageDelta.Usage; u != nil {
			mergeUsage(&s.message.Usage, u)
		}
	}
}

// mergeUsage lays a message_delta's usage over the reply's: each field the delta reports replaces the
// reply's.
func mergeUsage(dst *Usage, delta *MessageDeltaUsage) {
	base, _ := json.Marshal(dst)
	var merged map[string]json.RawMessage
	_ = json.Unmarshal(base, &merged)
	var over map[string]json.RawMessage
	_ = json.Unmarshal(delta.raw, &over)
	if over == nil {
		patch, _ := json.Marshal(delta)
		_ = json.Unmarshal(patch, &over)
	}
	for k, v := range over {
		merged[k] = v
	}
	out, _ := json.Marshal(merged)
	var u Usage
	if json.Unmarshal(out, &u) == nil {
		*dst = u
	}
}
