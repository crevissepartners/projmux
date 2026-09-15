package metadata

import (
	"bytes"
	"encoding/json"
	"strconv"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// registryDecodeStage names where decodeRegistryDocument stopped. Each stage
// fixes which version accompanies the error, so the read path and the recovery
// classifier can keep answering exactly what the envelope-then-body order
// always answered.
type registryDecodeStage int

const (
	// registryEnvelopeMalformed: the schemaVersion envelope did not decode, so
	// there is no version (0) and nothing was classified.
	registryEnvelopeMalformed registryDecodeStage = iota
	// registrySchemaRefused: the envelope version was refused before any body
	// field was decoded.
	registrySchemaRefused
	// registryBodyMalformed: the version was accepted but the body did not
	// decode; the version is the envelope version.
	registryBodyMalformed
	// registryDecoded: the version was accepted and the body decoded.
	registryDecoded
)

// decodeRegistryDocument classifies the effective top-level schemaVersion of a
// registry document and only then decodes its body. Classification comes first
// so an unknown envelope is refused without this build reinterpreting fields it
// does not understand. An absent or null schemaVersion is 0, which is unknown
// rather than pre-release, so it is refused too.
//
// A current or migratable document is decoded once: scanTopLevelSchemaVersion
// reads the effective version without an encoding/json pass, and when that
// version is accepted the body decode is the only full parse. The scanner never
// decides an error or a refusal on its own. Whenever it is unsure, the version
// it found is refused, or that single body decode fails, the document goes
// through the original sequence -- envelope decode, classification, body
// decode -- so every refusal and every error, including its text and the
// version beside it, is the one that sequence produces. Those are cold paths,
// so their extra pass is not worth a second way of reporting them.
//
// The single decode is exact because json.Unmarshal validates the whole input
// before decoding any field: a document that is not valid JSON can never
// succeed on the fast path, and for valid JSON the scanner only answers when
// the envelope decode would succeed with the same version.
func decodeRegistryDocument(data []byte, migrations coremetadata.MigrationSet) (coremetadata.Registry, int, registryDecodeStage, error) {
	if version, ok := scanTopLevelSchemaVersion(data); ok {
		if _, err := coremetadata.ClassifySchemaVersionWith(migrations, version); err == nil {
			var registry coremetadata.Registry
			if err := json.Unmarshal(data, &registry); err == nil {
				return registry, version, registryDecoded, nil
			}
		}
	}

	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return coremetadata.Registry{}, 0, registryEnvelopeMalformed, err
	}
	if _, err := coremetadata.ClassifySchemaVersionWith(migrations, envelope.SchemaVersion); err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, registrySchemaRefused, err
	}
	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		return coremetadata.Registry{}, envelope.SchemaVersion, registryBodyMalformed, err
	}
	return registry, envelope.SchemaVersion, registryDecoded, nil
}

// registrySchemaVersionKey is the envelope key in encoding/json's ASCII case
// folding, which only uppercases a-z.
const registrySchemaVersionKey = "SCHEMAVERSION"

// scanTopLevelSchemaVersion reports the schemaVersion that decoding data into
// struct{ SchemaVersion int } would yield, without decoding it. Its contract
// holds for valid JSON only: when it returns (v, true) for valid JSON, that
// decode succeeds with v. It returns false whenever it cannot be sure, and it
// does not validate: invalid JSON may still return true, which is safe only
// because the caller's json.Unmarshal rejects it before decoding anything.
//
// It follows encoding/json's effective-field rules: only top-level members
// count, keys match case-insensitively, the last matching key wins, and null
// leaves the value unchanged. A key containing an escape or a non-ASCII byte is
// never interpreted, because encoding/json folds it with Unicode rules (U+017F
// folds to S); a matching value that is not null or an in-range integer literal
// is never interpreted, because the envelope decode fails on it.
func scanTopLevelSchemaVersion(data []byte) (int, bool) {
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return 0, false
	}
	i = skipJSONSpace(data, i+1)
	version := 0
	if i < len(data) && data[i] == '}' {
		return version, skipJSONSpace(data, i+1) == len(data)
	}
	for {
		if i >= len(data) || data[i] != '"' {
			return 0, false
		}
		end := bytes.IndexByte(data[i+1:], '"')
		if end < 0 {
			return 0, false
		}
		key := data[i+1 : i+1+end]
		matches, ok := foldsToSchemaVersionKey(key)
		if !ok {
			return 0, false
		}
		i = skipJSONSpace(data, i+1+end+1)
		if i >= len(data) || data[i] != ':' {
			return 0, false
		}
		i = skipJSONSpace(data, i+1)
		if i >= len(data) {
			return 0, false
		}
		if matches {
			next, value, isNull, ok := scanSchemaVersionValue(data, i)
			if !ok {
				return 0, false
			}
			if !isNull {
				version = value
			}
			i = next
		} else {
			next, ok := skipJSONValue(data, i)
			if !ok {
				return 0, false
			}
			i = next
		}
		i = skipJSONSpace(data, i)
		if i >= len(data) {
			return 0, false
		}
		switch data[i] {
		case ',':
			i = skipJSONSpace(data, i+1)
		case '}':
			if skipJSONSpace(data, i+1) != len(data) {
				return 0, false
			}
			return version, true
		default:
			return 0, false
		}
	}
}

// foldsToSchemaVersionKey reports whether a raw, still-quoted-content key is
// the schemaVersion key. ok is false for any key encoding/json would unescape
// or fold with Unicode rules, since this scanner does not reproduce either.
func foldsToSchemaVersionKey(key []byte) (matches, ok bool) {
	for _, c := range key {
		if c == '\\' || c < 0x20 || c >= 0x80 {
			return false, false
		}
	}
	if len(key) != len(registrySchemaVersionKey) {
		return false, true
	}
	for i, c := range key {
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c != registrySchemaVersionKey[i] {
			return false, true
		}
	}
	return true, true
}

// scanSchemaVersionValue reads the value of a matching key starting at i. It
// accepts only null and integer literals that fit in int, the two kinds the
// envelope decode stores without error.
func scanSchemaVersionValue(data []byte, i int) (next, value int, isNull, ok bool) {
	if bytes.HasPrefix(data[i:], []byte("null")) {
		return i + len("null"), 0, true, true
	}
	if c := data[i]; c != '-' && (c < '0' || c > '9') {
		return 0, 0, false, false
	}
	j := i
	for j < len(data) {
		c := data[j]
		if c == '.' || c == 'e' || c == 'E' || c == '+' {
			return 0, 0, false, false
		}
		if c != '-' && (c < '0' || c > '9') {
			break
		}
		j++
	}
	// Atoi is ParseInt at the platform int size, which is what the envelope
	// decode's ParseInt(..., 64) plus the int overflow check accepts.
	value, err := strconv.Atoi(string(data[i:j]))
	if err != nil {
		return 0, 0, false, false
	}
	return j, value, false, true
}

// jsonContainerSkipByte marks the only bytes that change state while skipping a
// container: string quotes and escapes, and brackets outside strings.
var jsonContainerSkipByte = [256]bool{'"': true, '\\': true, '{': true, '}': true, '[': true, ']': true}

// skipJSONValue returns the index just past the value starting at i, assuming
// the input is valid JSON there.
func skipJSONValue(data []byte, i int) (int, bool) {
	switch data[i] {
	case '"':
		return skipJSONString(data, i)
	case '{', '[':
		depth := 0
		inString := false
		for ; i < len(data); i++ {
			c := data[i]
			if !jsonContainerSkipByte[c] {
				continue
			}
			if inString {
				switch c {
				case '\\':
					i++ // the escaped byte can neither close the string nor open a container
				case '"':
					inString = false
				}
				continue
			}
			switch c {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			}
		}
		return 0, false
	default:
		// A number, true, false, or null runs to the next delimiter.
		j := i
		for j < len(data) {
			switch data[j] {
			case ' ', '\t', '\r', '\n', ',', '}', ']':
				return j, j > i
			}
			j++
		}
		return 0, false
	}
}

// skipJSONString returns the index just past the string whose opening quote is
// at i. A quote closes the string when an even number of backslashes precede it.
func skipJSONString(data []byte, i int) (int, bool) {
	j := i + 1
	for {
		end := bytes.IndexByte(data[j:], '"')
		if end < 0 {
			return 0, false
		}
		quote := j + end
		backslashes := 0
		for k := quote - 1; k > i && data[k] == '\\'; k-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return quote + 1, true
		}
		j = quote + 1
	}
}

// skipJSONSpace returns the first index at or after i that is not JSON
// whitespace.
func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}
