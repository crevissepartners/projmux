package app

import "time"

// This fixture pacing mirrors the existing broker owned-read admission interval.
// It is not a freshness receipt: the public start still performs its own fresh
// read. The repo broker/observer regression pins the exact one-second boundary.
const installedRecoveryOwnedReadInterval = time.Second

type installedRecoveryAdmission struct {
	identity installedRecoveryAgent
	since    time.Time
}

func (admission *installedRecoveryAdmission) observe(now time.Time, out installedRecoveryObservation) installedRecoveryObservation {
	if out.Stage != "ready" {
		*admission = installedRecoveryAdmission{}
		return out
	}
	if out.Control == nil || !out.Control.OK || !out.Control.Availability.Start {
		*admission = installedRecoveryAdmission{}
		out.Stage = "control-not-startable"
		return out
	}
	if admission.since.IsZero() || admission.identity != out.Identity || now.Before(admission.since) {
		admission.identity, admission.since = out.Identity, now
	}
	if now.Sub(admission.since) < installedRecoveryOwnedReadInterval {
		out.Stage = "waiting-read-admission"
	}
	return out
}
