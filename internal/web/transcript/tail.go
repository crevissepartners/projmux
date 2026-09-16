package transcript

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// Tailer yields new turns appended to a transcript since the last read.
//
// Transcripts are append-only jsonl written by a live provider, so a poll
// cannot assume it lands on a line boundary: the last chunk is usually a
// half-written record. Incomplete bytes are held in `pending` until their
// newline arrives rather than being parsed and dropped.
type Tailer struct {
	path     string
	provider string
	parse    lineParser

	offset  int64
	pending []byte
}

// NewTailer starts following path. When fromStart is false it begins at the
// current end of file, which is what a UI wants after it has already rendered
// a transcript tail: only genuinely new turns arrive.
func NewTailer(provider, path string, fromStart bool) (*Tailer, error) {
	t := &Tailer{path: path, provider: provider, parse: parserFor(provider)}
	if fromStart {
		return t, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	t.offset = info.Size()
	return t, nil
}

// NewTailerAt starts following path at offset, which is where a previous
// ReadTranscript or Tailer left off. An offset past the end of the file means
// the file was replaced, and reading starts over.
func NewTailerAt(provider, path string, offset int64) *Tailer {
	if offset < 0 {
		offset = 0
	}
	return &Tailer{path: path, provider: provider, parse: parserFor(provider), offset: offset}
}

// Offset is the position after the last complete line returned. Bytes of a
// line still being written are not counted, so a follower resumed here reads
// that line whole.
func (t *Tailer) Offset() int64 {
	return t.offset - int64(len(t.pending))
}

// Next returns the turns appended since the previous call.
//
// A missing file is not an error: a provider can rotate or recreate its
// transcript, and the next poll picks it up from the start.
func (t *Tailer) Next() ([]Turn, error) {
	info, err := os.Stat(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	size := info.Size()
	switch {
	case size == t.offset:
		return nil, nil
	case size < t.offset:
		// Truncated or replaced; anything buffered belongs to the old file.
		t.offset = 0
		t.pending = nil
	}

	file, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	chunk := make([]byte, size-t.offset)
	read, err := io.ReadFull(file, chunk)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	chunk = chunk[:read]
	t.offset += int64(read)

	data := append(t.pending, chunk...)
	lastBreak := bytes.LastIndexByte(data, '\n')
	if lastBreak < 0 {
		// Still no complete line; keep waiting rather than parsing a fragment.
		t.pending = data
		return nil, nil
	}
	complete := data[:lastBreak]
	t.pending = append([]byte(nil), data[lastBreak+1:]...)

	var turns []Turn
	for raw := range bytes.SplitSeq(complete, []byte{'\n'}) {
		line := strings.TrimSpace(string(raw))
		if line == "" || line[0] != '{' {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if turn, ok := t.parse(record); ok {
			turns = append(turns, turn)
		}
	}
	return turns, nil
}
