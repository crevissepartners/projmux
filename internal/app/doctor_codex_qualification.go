package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

type doctorCodexQualification struct {
	Status       string                         `json:"status"`
	VersionPairs []doctorCodexQualificationPair `json:"version_pairs"`
}

type doctorCodexQualificationPair struct {
	OldVersion         string                               `json:"old_version,omitempty"`
	NewVersion         string                               `json:"new_version,omitempty"`
	Status             string                               `json:"status"`
	Verdict            codexgeneration.QualificationVerdict `json:"verdict,omitempty"`
	Reason             codexgeneration.QualificationReason  `json:"reason,omitempty"`
	QualificationReady *bool                                `json:"qualification_ready,omitempty"`
}

// This store read has no journal or Registry dependency: a receipt remains
// reportable even when neither exists. Schema v2 has no expiry semantics.
func (c *doctorCommand) readCodexQualification() *doctorCodexQualification {
	paths, err := configPaths(os.UserHomeDir, c.getenv)
	if err != nil {
		return &doctorCodexQualification{Status: "unavailable", VersionPairs: []doctorCodexQualificationPair{}}
	}
	return diagnoseCodexQualification(codexupgrade.NewQualificationStateStore(paths.StateDir))
}

func diagnoseCodexQualification(store *codexupgrade.QualificationStore) *doctorCodexQualification {
	report := &doctorCodexQualification{Status: "absent", VersionPairs: []doctorCodexQualificationPair{}}
	entries, err := store.List()
	if err != nil {
		report.Status = "unavailable"
		return report
	}
	for _, entry := range entries {
		row := doctorCodexQualificationPair{
			OldVersion: entry.Versions.Old, NewVersion: entry.Versions.New, Status: "stored",
		}
		switch {
		case errors.Is(entry.Err, codexupgrade.ErrQualificationVersionPairMismatch):
			row.Status = "version-pair-mismatch"
		case entry.Err != nil:
			row.Status = "damaged"
		default:
			row.Verdict, row.Reason = entry.Result.Verdict, entry.Result.Reason
			ready := codexgeneration.GateQualification(entry.Result).Phase2Ready
			row.QualificationReady = &ready
		}
		if row.Status != "stored" {
			report.Status = "damaged"
		} else if report.Status == "absent" {
			report.Status = "stored"
		}
		report.VersionPairs = append(report.VersionPairs, row)
	}
	return report
}

func writeDoctorCodexQualificationText(buf *bytes.Buffer, report *doctorCodexQualification) {
	if report == nil {
		return
	}
	fmt.Fprintf(buf, "\nCodex stored version-pair qualification\n  Store: %s\n", report.Status)
	for _, row := range report.VersionPairs {
		pair := "unrecognized receipt filename"
		if row.OldVersion != "" && row.NewVersion != "" {
			pair = row.OldVersion + " -> " + row.NewVersion
		}
		fmt.Fprintf(buf, "  %s: %s", pair, row.Status)
		if row.QualificationReady != nil {
			fmt.Fprintf(buf, "; verdict: %s; reason: %s; qualification ready: %t", row.Verdict, row.Reason, *row.QualificationReady)
		}
		buf.WriteByte('\n')
	}
}

// These are the content-free fields of this projection only. Keeping version
// tokens scoped to version_pairs leaves other support-report identifiers hashed.
func safeCodexQualificationString(parentKey, childKey, value string) bool {
	if parentKey != "codex_stored_qualification" && parentKey != "version_pairs" {
		return false
	}
	switch childKey {
	case "status":
		return value == "absent" || value == "stored" || value == "damaged" || value == "unavailable" || value == "version-pair-mismatch"
	case "old_version", "new_version":
		return codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: value, New: value}, codexgeneration.QualificationEvidence{}).Validate() == nil
	case "verdict":
		return value == string(codexgeneration.VerdictYes) || value == string(codexgeneration.VerdictNo)
	case "reason":
		switch codexgeneration.QualificationReason(value) {
		case codexgeneration.ReasonQualified, codexgeneration.ReasonDistinctThreadFailed,
			codexgeneration.ReasonSameThreadOwnershipFailed, codexgeneration.ReasonBundleLeaseFailed,
			codexgeneration.ReasonAuthConfigIsolationFailed, codexgeneration.ReasonProtocolMismatch:
			return true
		}
	}
	return false
}
