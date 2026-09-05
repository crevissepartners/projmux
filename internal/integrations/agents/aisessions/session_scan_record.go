package aisessions

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// sessionScanRecord materializes only the fields still needed by candidate
// discovery. Large assistant/tool payloads stay raw instead of allocating a
// recursive map[string]any tree on every line. The raw field dictionary keeps
// JSON key matching case-sensitive and ignores non-string aliases, as the
// original scanner did; a fixed JSON struct alone would change both behaviors.
// Title candidates still go through the existing provider title helpers.
type sessionScanRecord struct {
	id, cwd, branch   string
	title             string
	titleProvenance   TitleProvenance
	explicitSessionID string
}

func decodeSessionScanRecord(line []byte, details sessionDetails, provider string) (sessionScanRecord, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return sessionScanRecord{}, err
	}
	// RawMessage does not convert numbers to float64. Preserve the former
	// whole-record rejection of an overflowing number, even in an unused field.
	if err := sessionScanNumberError(line); err != nil {
		return sessionScanRecord{}, err
	}
	var record sessionScanRecord
	if details.id == "" {
		record.id = firstNestedRawString(fields, "sessionId", "session_id", "id")
	}
	if details.cwd == "" {
		record.cwd = firstNestedRawString(fields, "cwd", "current_dir", "currentDir", "project_dir", "projectDir", "project_path", "projectPath", "working_directory", "workingDirectory")
	}
	if details.branch == "" {
		record.branch = firstNestedRawString(fields, "gitBranch", "git_branch", "branch")
	}
	seekCanonical := (provider == AgentClaude && details.titleProvenance != TitleExplicitProvider) ||
		(provider == AgentCodex && details.titleProvenance != TitleDerivedUserPrompt)
	if details.title == "" || seekCanonical || titleIsResumeID(details.title, record.id) {
		recordType := strings.ToLower(rawScanString(fields["type"]))
		id := details.id
		if id == "" {
			id = record.id
		}
		// A later Claude prompt/tool-result cannot replace an existing label;
		// only the canonical ai-title can. Keep seeking that record through the
		// unchanged line window without decoding unused transcript content.
		if provider == AgentClaude && details.title != "" && !titleIsResumeID(details.title, id) && recordType != "ai-title" {
			return record, nil
		}
		switch recordType {
		case "ai-title", "event_msg", "response_item", "user":
		default:
			if !strings.EqualFold(rawScanString(fields["role"]), "user") {
				return record, nil
			}
		}
		var titleFields map[string]any
		if err := json.Unmarshal(line, &titleFields); err != nil {
			return sessionScanRecord{}, err
		}
		record.title = titleFromRecord(titleFields)
		record.titleProvenance = titleProvenanceFromRecord(titleFields)
		record.explicitSessionID = stringJSONField(titleFields, "sessionId")
	}
	return record, nil
}

func rawScanString(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNestedRawString(fields map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		if value := rawScanString(fields[key]); value != "" {
			return value
		}
	}
	for _, raw := range fields {
		if len(raw) == 0 || raw[0] != '{' || !rawScanMayContainKey(raw, keys) {
			continue
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			continue
		}
		if value := firstNestedRawString(nested, keys...); value != "" {
			return value
		}
	}
	return ""
}

func rawScanMayContainKey(raw []byte, keys []string) bool {
	// Escaped keys must use the ordinary JSON decoder. A token in string content
	// can be a false positive here; it only causes extra decoding, never omission.
	if bytes.Contains(raw, []byte(`\u`)) {
		return true
	}
	for _, key := range keys {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			return true
		}
	}
	return false
}

func sessionScanNumberError(line []byte) error {
	for i := 0; i < len(line); i++ {
		if line[i] == '"' {
			// JSON has already been validated. Skip string content, including escaped
			// quotes, so transcript text that resembles a number cannot reject a row.
			for i++; i < len(line); i++ {
				if line[i] == '\\' {
					i++
					continue
				}
				if line[i] == '"' {
					break
				}
			}
		} else if line[i] == '-' || (line[i] >= '0' && line[i] <= '9') {
			start := i
			exponent := false
			for i < len(line) && ((line[i] >= '0' && line[i] <= '9') || line[i] == '-' || line[i] == '+' || line[i] == '.' || line[i] == 'e' || line[i] == 'E') {
				exponent = exponent || line[i] == 'e' || line[i] == 'E'
				i++
			}
			if exponent || i-start >= 309 {
				if _, err := strconv.ParseFloat(string(line[start:i]), 64); err != nil {
					return err
				}
			}
			i--
		}
	}
	return nil
}
