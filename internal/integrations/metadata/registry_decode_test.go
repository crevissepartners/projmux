package metadata

import (
	"encoding/json"
	"errors"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

type scanSchemaVersionRow struct {
	name        string
	data        string
	wantVersion int
	wantOK      bool
}

// scanSchemaVersionRows is shared by the scanner table and the contract fuzz
// seeds. A true row is a document whose envelope the scanner must read
// exactly; a false row is one it must leave to encoding/json.
var scanSchemaVersionRows = []scanSchemaVersionRow{
	{name: "only-key", data: `{"schemaVersion":4}`, wantVersion: 4, wantOK: true},
	{name: "first-key", data: `{"schemaVersion": 4, "apiVersion": "projmux.io/v1alpha1"}`, wantVersion: 4, wantOK: true},
	{name: "middle-key", data: `{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 3, "projects": []}`, wantVersion: 3, wantOK: true},
	{name: "last-key", data: `{"apiVersion": "projmux.io/v1alpha1", "projects": [], "schemaVersion": 2}`, wantVersion: 2, wantOK: true},
	{name: "json-whitespace-everywhere", data: " \t\r\n{ \t\r\n\"schemaVersion\" \t\r\n: \t\r\n4 \t\r\n} \t\r\n", wantVersion: 4, wantOK: true},
	{name: "empty-object", data: `{}`, wantVersion: 0, wantOK: true},
	{name: "empty-object-spaced", data: "{ \n }\n", wantVersion: 0, wantOK: true},
	{name: "absent-key", data: `{"apiVersion": "projmux.io/v1alpha1"}`, wantVersion: 0, wantOK: true},
	{name: "nested-object-ignored", data: `{"extensions": {"schemaVersion": 5}, "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "nested-only", data: `{"extensions": {"schemaVersion": 5}}`, wantVersion: 0, wantOK: true},
	{name: "nested-array-ignored", data: `{"list": [{"schemaVersion": 5}, ["schemaVersion", 5], []], "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "deep-nesting-ignored", data: `{"a": [[{"b": {"schemaVersion": 5}}]], "schemaVersion": 4, "c": {"d": [{"schemaVersion": 6}]}}`, wantVersion: 4, wantOK: true},
	{name: "key-inside-string-ignored", data: `{"note": "\"schemaVersion\": 5", "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "escaped-backslash-before-closing-quote", data: `{"a": "x\\", "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "escaped-quote-then-backslashes", data: `{"a": "say \"}\" \\\"{", "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "brackets-inside-container-strings", data: `{"a": ["}", "{", "]\"[", {"k\"}": "\\"}], "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "non-ascii-value", data: `{"name": "é ſ K", "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "skipped-literals", data: `{"a": true, "b": false, "c": null, "d": -1.5e+3, "e": 0, "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "near-miss-keys", data: `{"schemaVersions": 5, "schemaVersio": 5, "schema_version": 5, "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "case-variant-title", data: `{"SchemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "case-variant-upper", data: `{"SCHEMAVERSION": 5}`, wantVersion: 5, wantOK: true},
	{name: "case-variant-lower", data: `{"schemaversion": 3}`, wantVersion: 3, wantOK: true},
	{name: "duplicate-last-wins", data: `{"schemaVersion": 4, "schemaVersion": 5}`, wantVersion: 5, wantOK: true},
	{name: "duplicate-case-variant-last-wins", data: `{"SCHEMAVERSION": 5, "schemaVersion": 4}`, wantVersion: 4, wantOK: true},
	{name: "null-only", data: `{"schemaVersion": null}`, wantVersion: 0, wantOK: true},
	{name: "null-after-int-keeps-int", data: `{"schemaVersion": 4, "schemaVersion": null}`, wantVersion: 4, wantOK: true},
	{name: "int-after-null", data: `{"schemaVersion": null, "SchemaVersion": 3}`, wantVersion: 3, wantOK: true},
	{name: "zero", data: `{"schemaVersion": 0}`, wantVersion: 0, wantOK: true},
	{name: "negative", data: `{"schemaVersion": -1}`, wantVersion: -1, wantOK: true},
	{name: "negative-zero", data: `{"schemaVersion": -0}`, wantVersion: 0, wantOK: true},
	{name: "max-int", data: `{"schemaVersion": 9223372036854775807}`, wantVersion: 9223372036854775807, wantOK: true},
	{name: "min-int", data: `{"schemaVersion": -9223372036854775808}`, wantVersion: -9223372036854775808, wantOK: true},

	{name: "top-level-array", data: `[{"schemaVersion": 4}]`},
	{name: "top-level-string", data: `"schemaVersion"`},
	{name: "top-level-number", data: `4`},
	{name: "top-level-null", data: `null`},
	{name: "top-level-true", data: `true`},
	{name: "empty-input", data: ``},
	{name: "whitespace-input", data: " \n\t\r"},
	{name: "utf8-bom", data: "\xef\xbb\xbf{\"schemaVersion\": 4}"},
	{name: "non-json-whitespace", data: "\v{\"schemaVersion\": 4}"},
	{name: "float", data: `{"schemaVersion": 4.0}`},
	{name: "exponent", data: `{"schemaVersion": 4e0}`},
	{name: "exponent-signed", data: `{"schemaVersion": 40E-1}`},
	{name: "exponent-plus", data: `{"schemaVersion": 4e+0}`},
	{name: "string-value", data: `{"schemaVersion": "4"}`},
	{name: "bool-value", data: `{"schemaVersion": true}`},
	{name: "object-value", data: `{"schemaVersion": {}}`},
	{name: "array-value", data: `{"schemaVersion": [4]}`},
	{name: "bad-value-behind-good", data: `{"schemaVersion": 4, "SCHEMAVERSION": "5"}`},
	{name: "overflow", data: `{"schemaVersion": 9223372036854775808}`},
	{name: "underflow", data: `{"schemaVersion": -9223372036854775809}`},
	{name: "overflow-long", data: `{"schemaVersion": 99999999999999999999}`},
	{name: "escaped-key", data: `{"schema` + "\\" + `u0056ersion": 4}`},
	{name: "escaped-non-matching-key", data: `{"a\"b": 1, "schemaVersion": 4}`},
	{name: "non-ascii-folding-key", data: `{"ſchemaVersion": 4}`},
	{name: "non-ascii-folding-key-behind-exact", data: `{"schemaVersion": 4, "ſchemaVersion": 5}`},
	{name: "non-ascii-non-matching-key", data: `{"é": 1, "schemaVersion": 4}`},
	{name: "missing-colon", data: `{"schemaVersion" 4}`},
	{name: "missing-comma", data: `{"schemaVersion": 4 "a": 1}`},
	{name: "trailing-comma", data: `{"schemaVersion": 4,}`},
	{name: "leading-comma", data: `{,"schemaVersion": 4}`},
	{name: "unterminated-object", data: `{"schemaVersion": 4`},
	{name: "unterminated-key", data: `{"schemaVersion`},
	{name: "unterminated-container", data: `{"a": [1, 2, "schemaVersion": 4}`},
	{name: "unquoted-key", data: `{schemaVersion: 4}`},
	{name: "truncated-null", data: `{"schemaVersion": nul}`},
	{name: "extra-closing-brace", data: `{"schemaVersion": 4}}`},
	{name: "trailing-garbage", data: `{"schemaVersion": 4} x`},
	{name: "trailing-second-document", data: `{"schemaVersion": 4}{"schemaVersion": 5}`},
	{name: "sign-only", data: `{"schemaVersion": -}`},
	{name: "embedded-minus", data: `{"schemaVersion": 4-5}`},
}

// envelopeSchemaVersion is the envelope decode the scanner stands in for.
func envelopeSchemaVersion(data []byte) (int, error) {
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	err := json.Unmarshal(data, &envelope)
	return envelope.SchemaVersion, err
}

func TestScanTopLevelSchemaVersion(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, row := range scanSchemaVersionRows {
		if seen[row.name] {
			t.Fatalf("duplicate scanner row %q", row.name)
		}
		seen[row.name] = true
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			data := []byte(row.data)
			version, ok := scanTopLevelSchemaVersion(data)
			if version != row.wantVersion || ok != row.wantOK {
				t.Fatalf("scanTopLevelSchemaVersion(%q) = %d, %t; want %d, %t", row.data, version, ok, row.wantVersion, row.wantOK)
			}
			if !ok {
				return
			}
			// Hold every true row to encoding/json itself, so the table cannot
			// encode a wrong belief about the envelope decode.
			if !json.Valid(data) {
				t.Fatalf("true row %q is not valid JSON", row.data)
			}
			want, err := envelopeSchemaVersion(data)
			if err != nil || want != version {
				t.Fatalf("envelope decode of %q = %d, %v; scanner = %d", row.data, want, err, version)
			}
		})
	}
}

// FuzzScanTopLevelSchemaVersionMatchesEnvelopeDecode holds the scanner to its
// contract: for valid JSON, a true answer means the envelope decode succeeds
// with the same version.
func FuzzScanTopLevelSchemaVersionMatchesEnvelopeDecode(f *testing.F) {
	for _, row := range registryReadFenceRows() {
		f.Add(row.data)
	}
	for _, row := range scanSchemaVersionRows {
		f.Add([]byte(row.data))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		version, ok := scanTopLevelSchemaVersion(data)
		if !ok || !json.Valid(data) {
			return
		}
		want, err := envelopeSchemaVersion(data)
		if err != nil {
			t.Fatalf("scanner read version %d from %q, but the envelope decode fails: %v", version, data, err)
		}
		if want != version {
			t.Fatalf("scanner read version %d from %q, envelope decode reads %d", version, data, want)
		}
	})
}

func TestDecodeRegistryDocumentStages(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name        string
		data        []byte
		wantStage   registryDecodeStage
		wantVersion int
		wantErr     func(error) bool
	}{
		// An unknown version is refused before the body decode, so the body
		// type error behind it is never the answer.
		{name: "newer-with-body-type-error", data: []byte(`{"schemaVersion": 5, "projects": "x"}`),
			wantStage: registrySchemaRefused, wantVersion: 5, wantErr: isErr(coremetadata.ErrSchemaTooNew)},
		{name: "negative-with-body-type-error", data: []byte(`{"schemaVersion": -1, "projects": "x"}`),
			wantStage: registrySchemaRefused, wantVersion: -1, wantErr: isErr(coremetadata.ErrSchemaUnsupported)},
		{name: "zero-with-body-type-error", data: []byte(`{"schemaVersion": 0, "projects": "x"}`),
			wantStage: registrySchemaRefused, wantVersion: 0, wantErr: isErr(coremetadata.ErrSchemaUnsupported)},
		{name: "absent-with-body-type-error", data: []byte(`{"projects": "x"}`),
			wantStage: registrySchemaRefused, wantVersion: 0, wantErr: isErr(coremetadata.ErrSchemaUnsupported)},
		{name: "null-with-body-type-error", data: []byte(`{"schemaVersion": null, "projects": "x"}`),
			wantStage: registrySchemaRefused, wantVersion: 0, wantErr: isErr(coremetadata.ErrSchemaUnsupported)},
		// The scanner accepts this version without validating the rest, so
		// the single body decode fails on the syntax error and the envelope
		// decode then reports it with no version.
		{name: "known-with-later-syntax-error", data: []byte(`{"schemaVersion": 4, "projects": tru}`),
			wantStage: registryEnvelopeMalformed, wantVersion: 0, wantErr: isSyntaxError},
		{name: "string-version", data: []byte(`{"schemaVersion": "4"}`),
			wantStage: registryEnvelopeMalformed, wantVersion: 0, wantErr: isTypeError},
		{name: "current-with-body-type-error", data: []byte(`{"schemaVersion": 4, "projects": "x"}`),
			wantStage: registryBodyMalformed, wantVersion: 4, wantErr: isTypeError},
		{name: "migration-with-body-type-error", data: []byte(`{"schemaVersion": 3, "projects": "x"}`),
			wantStage: registryBodyMalformed, wantVersion: 3, wantErr: isTypeError},
		{name: "current", data: fenceEnvelope(`"schemaVersion": 4`, ""),
			wantStage: registryDecoded, wantVersion: 4, wantErr: isNil},
		{name: "migration-v1", data: []byte(fenceV1Registry),
			wantStage: registryDecoded, wantVersion: 1, wantErr: isNil},
		// The scanner leaves an escaped key to encoding/json, and the fallback
		// still accepts the document it names.
		{name: "escaped-key-current", data: fenceEnvelope(`"schema`+"\\"+`u0056ersion": 4`, ""),
			wantStage: registryDecoded, wantVersion: 4, wantErr: isNil},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			registry, version, stage, err := decodeRegistryDocument(row.data, coremetadata.ProductionMigrationSet())
			if stage != row.wantStage || version != row.wantVersion {
				t.Fatalf("decodeRegistryDocument stage/version = %d/%d, want %d/%d (err %v)", stage, version, row.wantStage, row.wantVersion, err)
			}
			if !row.wantErr(err) {
				t.Fatalf("decodeRegistryDocument error = %#v", err)
			}
			if stage != registryDecoded {
				if registry.SchemaVersion != 0 || registry.APIVersion != "" || registry.Projects != nil {
					t.Fatalf("stage %d returned a decoded registry: %+v", stage, registry)
				}
				return
			}
			if registry.SchemaVersion != version || len(registry.Projects) == 0 {
				t.Fatalf("decoded registry = schemaVersion %d with %d projects, want schemaVersion %d with projects", registry.SchemaVersion, len(registry.Projects), version)
			}
		})
	}

	// The known-version syntax-error row must reach the envelope decode through
	// a failed single decode, not through the scanner.
	if _, ok := scanTopLevelSchemaVersion([]byte(`{"schemaVersion": 4, "projects": tru}`)); !ok {
		t.Fatal("scanner no longer accepts the known-with-later-syntax-error row; it no longer covers the fallback after a failed single decode")
	}
}

func isErr(target error) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) }
}

func isSyntaxError(err error) bool {
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr)
}

func isTypeError(err error) bool {
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func isNil(err error) bool { return err == nil }
