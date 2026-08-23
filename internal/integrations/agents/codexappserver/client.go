package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxFrameBytes       = 1 << 20
	notificationBacklog = 64
	writeCancelSettle   = 5 * time.Millisecond
)

var (
	ErrUnsupported  = errors.New("codex app-server method unsupported")
	ErrProtocol     = errors.New("codex app-server protocol error")
	ErrDisconnected = errors.New("codex app-server disconnected")
)

type readWriteCloser interface {
	io.Reader
	io.Writer
	io.Closer
}

type streamAborter interface {
	abort() error
}

// Client is a minimal concurrent JSONL client for one initialized app-server
// connection. It owns request IDs, cancellation, response routing, and the
// bounded notification reader.
type Client struct {
	stream readWriteCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response
	events  chan Notification
	done    chan struct{}
	err     error
	version string
	once    sync.Once
}

// NewClient starts a bounded reader over stream. The caller must Initialize
// before sending any other request.
func NewClient(stream readWriteCloser) *Client {
	c := &Client{
		stream:  stream,
		nextID:  1,
		pending: make(map[int64]chan response),
		events:  make(chan Notification, notificationBacklog),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Notifications returns the bounded typed notification stream for this
// initialized connection. Params contains only the method payload bytes; a
// capability consumer must decode and immediately project them into its own
// safe domain type rather than persisting the raw payload.
func (c *Client) Notifications() <-chan Notification { return c.events }

// Initialize performs the required single initialize/initialized handshake.
func (c *Client) Initialize(ctx context.Context, version string) (string, error) {
	var result initializeResult
	err := c.Request(ctx, methodInitialize, initializeParams{ClientInfo: clientInfo{
		Name: "projmux", Title: "Projmux", Version: safeVersion(version),
	}}, &result)
	if err != nil {
		return "", err
	}
	if err := c.notify(ctx, methodInitialized, struct{}{}); err != nil {
		return "", err
	}
	negotiatedVersion := safeVersion(result.UserAgent)
	c.mu.Lock()
	c.version = negotiatedVersion
	c.mu.Unlock()
	return negotiatedVersion, nil
}

// Request sends one request and waits until its matching response, local
// cancellation, disconnect, or protocol failure. Late responses to cancelled
// IDs are ignored.
func (c *Client) Request(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	id := c.nextID
	c.nextID++
	wait := make(chan response, 1)
	c.pending[id] = wait
	c.mu.Unlock()

	if err := c.writeJSONContext(ctx, wireRequest{Method: method, ID: id, Params: params}); err != nil {
		c.cancelPending(id)
		return err
	}

	select {
	case <-ctx.Done():
		c.cancelPending(id)
		return ctx.Err()
	case reply := <-wait:
		if reply.err != nil {
			return reply.err
		}
		if result == nil || len(reply.result) == 0 || string(reply.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(reply.result, result); err != nil {
			return fmt.Errorf("%w: invalid response result", ErrProtocol)
		}
		return nil
	}
}

func (c *Client) abort() error {
	if aborter, ok := c.stream.(streamAborter); ok {
		return aborter.abort()
	}
	return c.stream.Close()
}

// Close terminates the connection and unblocks all pending requests.
func (c *Client) Close() error {
	err := c.stream.Close()
	c.fail(ErrDisconnected)
	return err
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.writeJSONContext(ctx, wireNotification{Method: method, Params: params})
}

func (c *Client) writeJSONContext(ctx context.Context, message any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- c.writeJSON(message)
	}()
	select {
	case err := <-writeDone:
		return err
	case <-ctx.Done():
	}

	// A peer can finish reading just before cancellation while the writer is
	// waiting to be scheduled. Give that completed write one fixed, bounded
	// chance to settle so ordinary request cancellation keeps the connection
	// and ignores its late response.
	timer := time.NewTimer(writeCancelSettle)
	defer timer.Stop()
	select {
	case err := <-writeDone:
		if err != nil {
			return err
		}
		return ctx.Err()
	case <-timer.C:
	}

	_ = c.abort()
	c.fail(ErrDisconnected)
	// Closing the owned transport is the write-cancellation contract: it
	// unblocks the writer before this method returns, leaving no goroutine.
	<-writeDone
	return ctx.Err()
}

func (c *Client) writeJSON(message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("%w: encode request", ErrProtocol)
	}
	if len(data)+1 > maxFrameBytes {
		return fmt.Errorf("%w: request frame too large", ErrProtocol)
	}
	data = append(data, '\n')
	if _, err := c.stream.Write(data); err != nil {
		c.fail(ErrDisconnected)
		return ErrDisconnected
	}
	return nil
}

func (c *Client) readLoop() {
	scanner := bufio.NewScanner(c.stream)
	scanner.Buffer(make([]byte, 4096), maxFrameBytes)
	for scanner.Scan() {
		if err := c.routeFrame(scanner.Bytes()); err != nil {
			c.fail(err)
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("%w: read frame", ErrProtocol))
		return
	}
	c.fail(ErrDisconnected)
}

func (c *Client) routeFrame(frame []byte) error {
	var envelope wireEnvelope
	if len(frame) == 0 || json.Unmarshal(frame, &envelope) != nil {
		return fmt.Errorf("%w: malformed JSON", ErrProtocol)
	}
	if envelope.Method != "" {
		if len(envelope.Result) != 0 || envelope.Error != nil {
			return fmt.Errorf("%w: invalid server message shape", ErrProtocol)
		}
		requestID, err := normalizeServerRequestID(envelope.ID)
		if err != nil {
			return err
		}
		event := Notification{
			Method: envelope.Method, Params: append(json.RawMessage(nil), envelope.Params...), RequestID: requestID,
			RawRequestID: append(json.RawMessage(nil), envelope.ID...),
		}
		c.mu.Lock()
		if c.err != nil {
			err := c.err
			c.mu.Unlock()
			return err
		}
		select {
		case c.events <- event:
			c.mu.Unlock()
			return nil
		default:
			c.mu.Unlock()
			return fmt.Errorf("%w: notification backlog exceeded", ErrProtocol)
		}
	}
	if len(envelope.ID) == 0 || envelope.Method != "" || (len(envelope.Result) == 0) == (envelope.Error == nil) {
		return fmt.Errorf("%w: invalid message shape", ErrProtocol)
	}
	var id int64
	if err := json.Unmarshal(envelope.ID, &id); err != nil {
		return fmt.Errorf("%w: invalid response id", ErrProtocol)
	}
	c.mu.Lock()
	wait, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}
	if envelope.Error != nil {
		err := fmt.Errorf("%w", ErrProtocol)
		if envelope.Error.Code == -32601 {
			err = ErrUnsupported
		}
		wait <- response{err: err}
		return nil
	}
	wait <- response{result: append(json.RawMessage(nil), envelope.Result...)}
	return nil
}

// RespondServerRequest writes one response using the exact scalar request id
// received from the server. The caller owns response-once state; this method
// deliberately refuses reconstructed, object, array, null, fractional, and
// out-of-range ids before touching the wire.
func (c *Client) RespondServerRequest(ctx context.Context, rawID json.RawMessage, result any) error {
	if _, err := normalizeServerRequestID(rawID); err != nil || len(rawID) == 0 {
		if err == nil {
			err = fmt.Errorf("%w: missing server request id", ErrProtocol)
		}
		return err
	}
	return c.writeJSONContext(ctx, wireServerResponse{ID: append(json.RawMessage(nil), rawID...), Result: result})
}

func normalizeServerRequestID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return "", fmt.Errorf("%w: empty server request id", ErrProtocol)
		}
		return text, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil || strings.TrimSpace(number.String()) == "" {
		return "", fmt.Errorf("%w: invalid server request id", ErrProtocol)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: server request id has trailing data", ErrProtocol)
	}
	if _, err := strconv.ParseInt(number.String(), 10, 64); err != nil {
		return "", fmt.Errorf("%w: server request id is not an int64", ErrProtocol)
	}
	return number.String(), nil
}

func (c *Client) cancelPending(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		pending := c.pending
		c.pending = make(map[int64]chan response)
		close(c.done)
		close(c.events)
		c.mu.Unlock()
		for _, wait := range pending {
			wait <- response{err: err}
		}
	})
}
