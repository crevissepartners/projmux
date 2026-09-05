package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- required by the RFC 6455 handshake, not used for security.
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// maxDiscardBytes bounds how far past the retained limit this stream will read
// an over-long message before it treats the peer as broken. Dropping a message
// is what keeps one unreadable answer from retiring a shared connection, but an
// unbounded drop would let a peer hold the reader open forever.
const maxDiscardBytes = 64 << 20

type websocketStream struct {
	raw     readWriteCloser
	reader  *bufio.Reader
	writeMu sync.Mutex
	readBuf []byte
}

func upgradeProxyWebSocket(ctx context.Context, raw readWriteCloser) (*websocketStream, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("%w: create websocket nonce", ErrProtocol)
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	request := "GET / HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(raw, request)
		writeDone <- err
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-writeDone:
		if err != nil {
			return nil, ErrDisconnected
		}
	}

	reader := bufio.NewReader(raw)
	type result struct {
		response *http.Response
		err      error
	}
	readDone := make(chan result, 1)
	go func() {
		response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
		readDone <- result{response: response, err: err}
	}()
	var response *http.Response
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case got := <-readDone:
		if got.err != nil {
			return nil, ErrDisconnected
		}
		response = got.response
	}
	defer response.Body.Close()
	acceptSum := sha1.Sum([]byte(key + websocketGUID)) // #nosec G401 -- RFC 6455 protocol checksum.
	wantAccept := base64.StdEncoding.EncodeToString(acceptSum[:])
	if response.StatusCode != http.StatusSwitchingProtocols ||
		!strings.EqualFold(strings.TrimSpace(response.Header.Get("Upgrade")), "websocket") ||
		!headerContainsToken(response.Header.Get("Connection"), "upgrade") ||
		response.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		return nil, fmt.Errorf("%w: websocket upgrade rejected", ErrProtocol)
	}
	return &websocketStream{raw: raw, reader: reader}, nil
}

func headerContainsToken(value, want string) bool {
	for token := range strings.SplitSeq(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

func (s *websocketStream) Read(p []byte) (int, error) {
	for len(s.readBuf) == 0 {
		payload, oversize, err := s.readBoundedText(maxFrameBytes)
		if err != nil {
			return 0, err
		}
		if oversize {
			// A byte reader has nowhere to report a dropped message, so this
			// path keeps the original refusal. The client reads through
			// readBoundedText instead, which is told and can survive it.
			return 0, fmt.Errorf("%w: websocket frame too large", ErrProtocol)
		}
		s.readBuf = append(payload, '\n')
	}
	n := copy(p, s.readBuf)
	s.readBuf = s.readBuf[n:]
	return n, nil
}

// readBoundedText returns the next whole text message, answering control
// frames itself. A message larger than limit is reported as oversize with no
// payload; it has already been consumed, so the stream stays framed and the
// caller decides what that means.
func (s *websocketStream) readBoundedText(limit int) ([]byte, bool, error) {
	for {
		payload, opcode, oversize, err := s.readBoundedMessage(limit)
		if err != nil {
			return nil, false, err
		}
		switch opcode {
		case 0x1:
			return payload, oversize, nil
		case 0x8:
			return nil, false, io.EOF
		case 0x9:
			if err := s.writeFrame(0xA, payload); err != nil {
				return nil, false, err
			}
		case 0xA:
		default:
			return nil, false, fmt.Errorf("%w: unsupported websocket opcode", ErrProtocol)
		}
	}
}

func (s *websocketStream) Write(p []byte) (int, error) {
	payload := bytes.TrimSuffix(p, []byte{'\n'})
	if err := s.writeFrame(0x1, payload); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *websocketStream) Close() error {
	_ = s.writeFrame(0x8, nil)
	return s.raw.Close()
}

// abort bypasses the graceful close frame because a blocked data-frame write
// already owns writeMu. Closing the raw transport releases that writer.
func (s *websocketStream) abort() error {
	return s.raw.Close()
}

// readBoundedMessage reassembles one whole websocket message.
//
// limit bounds only what is retained. A message past the bound is read to its
// final fragment and dropped instead of ending the stream, because this
// transport is multiplexed across every bound thread and one answer this
// process cannot hold must not retire the connection the others are using.
// Dropping still consumes the exact byte count, so the next message starts on
// a frame boundary and the stream keeps its framing.
func (s *websocketStream) readBoundedMessage(limit int) ([]byte, byte, bool, error) {
	var complete []byte
	var messageOpcode byte
	oversize := false
	for {
		var header [2]byte
		if _, err := io.ReadFull(s.reader, header[:]); err != nil {
			return nil, 0, false, err
		}
		fin := header[0]&0x80 != 0
		opcode := header[0] & 0x0f
		if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
			return nil, 0, false, fmt.Errorf("%w: invalid websocket frame flags", ErrProtocol)
		}
		length := uint64(header[1] & 0x7f)
		switch length {
		case 126:
			var extended [2]byte
			if _, err := io.ReadFull(s.reader, extended[:]); err != nil {
				return nil, 0, false, err
			}
			length = uint64(binary.BigEndian.Uint16(extended[:]))
		case 127:
			var extended [8]byte
			if _, err := io.ReadFull(s.reader, extended[:]); err != nil {
				return nil, 0, false, err
			}
			length = binary.BigEndian.Uint64(extended[:])
		}
		if length > maxDiscardBytes {
			// Past this, reading the message out is itself the denial of
			// service the bound exists to stop.
			return nil, 0, false, fmt.Errorf("%w: websocket frame too large", ErrProtocol)
		}
		if limit > 0 && uint64(len(complete))+length > uint64(limit) {
			oversize = true
		}
		var payload []byte
		if oversize {
			complete = nil
			if _, err := io.CopyN(io.Discard, s.reader, int64(length)); err != nil {
				return nil, 0, false, err
			}
		} else {
			payload = make([]byte, int(length))
			if _, err := io.ReadFull(s.reader, payload); err != nil {
				return nil, 0, false, err
			}
		}
		if opcode >= 0x8 {
			if !fin || length > 125 {
				return nil, 0, false, fmt.Errorf("%w: invalid websocket control frame", ErrProtocol)
			}
			// A control frame may arrive between the fragments of a message.
			// It carries its own payload and never counts against the bound.
			return payload, opcode, false, nil
		}
		if opcode == 0x1 {
			if messageOpcode != 0 {
				return nil, 0, false, fmt.Errorf("%w: nested websocket message", ErrProtocol)
			}
			messageOpcode = opcode
		} else if opcode != 0x0 || messageOpcode == 0 {
			return nil, 0, false, fmt.Errorf("%w: invalid websocket continuation", ErrProtocol)
		}
		complete = append(complete, payload...)
		if fin {
			if oversize {
				return nil, messageOpcode, true, nil
			}
			return complete, messageOpcode, false, nil
		}
	}
}

func (s *websocketStream) writeFrame(opcode byte, payload []byte) error {
	lengthHeader, err := websocketFrameLengthHeader(len(payload))
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("%w: create websocket mask", ErrProtocol)
	}
	header := []byte{0x80 | opcode}
	header = append(header, lengthHeader...)
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%len(mask)]
	}
	if _, err := s.raw.Write(append(header, masked...)); err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return ErrDisconnected
		}
		return ErrDisconnected
	}
	return nil
}

func websocketFrameLengthHeader(length int) ([]byte, error) {
	if length < 0 || length > maxFrameBytes {
		return nil, fmt.Errorf("%w: websocket frame too large", ErrProtocol)
	}
	switch {
	case length <= 125:
		return []byte{0x80 | byte(length)}, nil // #nosec G115 -- length is validated in the 0..125 branch.
	case length <= 65535:
		var extended [2]byte
		binary.BigEndian.PutUint16(extended[:], uint16(length)) // #nosec G115 -- length is validated in the 126..65535 branch.
		return append([]byte{0x80 | 126}, extended[:]...), nil
	default:
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length)) // #nosec G115 -- length is non-negative and bounded by maxFrameBytes.
		return append([]byte{0x80 | 127}, extended[:]...), nil
	}
}
