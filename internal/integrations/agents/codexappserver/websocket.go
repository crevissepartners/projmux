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
		payload, opcode, err := s.readMessage()
		if err != nil {
			return 0, err
		}
		switch opcode {
		case 0x1:
			s.readBuf = append(payload, '\n')
		case 0x8:
			return 0, io.EOF
		case 0x9:
			if err := s.writeFrame(0xA, payload); err != nil {
				return 0, err
			}
		case 0xA:
			continue
		default:
			return 0, fmt.Errorf("%w: unsupported websocket opcode", ErrProtocol)
		}
	}
	n := copy(p, s.readBuf)
	s.readBuf = s.readBuf[n:]
	return n, nil
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

func (s *websocketStream) readMessage() ([]byte, byte, error) {
	var complete []byte
	var messageOpcode byte
	for {
		var header [2]byte
		if _, err := io.ReadFull(s.reader, header[:]); err != nil {
			return nil, 0, err
		}
		fin := header[0]&0x80 != 0
		opcode := header[0] & 0x0f
		if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
			return nil, 0, fmt.Errorf("%w: invalid websocket frame flags", ErrProtocol)
		}
		length := uint64(header[1] & 0x7f)
		switch length {
		case 126:
			var extended [2]byte
			if _, err := io.ReadFull(s.reader, extended[:]); err != nil {
				return nil, 0, err
			}
			length = uint64(binary.BigEndian.Uint16(extended[:]))
		case 127:
			var extended [8]byte
			if _, err := io.ReadFull(s.reader, extended[:]); err != nil {
				return nil, 0, err
			}
			length = binary.BigEndian.Uint64(extended[:])
		}
		if length > maxFrameBytes || uint64(len(complete))+length > maxFrameBytes {
			return nil, 0, fmt.Errorf("%w: websocket frame too large", ErrProtocol)
		}
		payload := make([]byte, int(length))
		if _, err := io.ReadFull(s.reader, payload); err != nil {
			return nil, 0, err
		}
		if opcode >= 0x8 {
			if !fin || length > 125 {
				return nil, 0, fmt.Errorf("%w: invalid websocket control frame", ErrProtocol)
			}
			return payload, opcode, nil
		}
		if opcode == 0x1 {
			if messageOpcode != 0 {
				return nil, 0, fmt.Errorf("%w: nested websocket message", ErrProtocol)
			}
			messageOpcode = opcode
		} else if opcode != 0x0 || messageOpcode == 0 {
			return nil, 0, fmt.Errorf("%w: invalid websocket continuation", ErrProtocol)
		}
		complete = append(complete, payload...)
		if fin {
			return complete, messageOpcode, nil
		}
	}
}

func (s *websocketStream) writeFrame(opcode byte, payload []byte) error {
	if len(payload) > maxFrameBytes {
		return fmt.Errorf("%w: websocket frame too large", ErrProtocol)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("%w: create websocket mask", ErrProtocol)
	}
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) <= 125:
		header = append(header, 0x80|byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, byte(len(payload)>>24), byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload)))
	}
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
