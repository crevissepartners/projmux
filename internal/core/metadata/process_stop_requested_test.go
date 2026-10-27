package metadata

import "testing"

func TestStopRequestedResumeGenerationAndRestore(t *testing.T) {
	reg, activation, receipt := resumableWriterFixture(t)
	m := Mutator{}
	if err := m.RecordProcessResumable(&reg, activation.Binding, &receipt); err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(activation.Binding.PaneUID)
	pane.Status.ProcessSession.StopRequested = true
	previous := pane.Status.ProcessSession.Clone()
	next := activation.Binding
	next.Generation, next.OperationID, next.HostInstanceID = "next-generation", "next-operation", "next-host"
	if err := m.ReserveProcessResume(&reg, previous.Binding, next); err != nil {
		t.Fatal("explicit resume refused stop mark:", err)
	}
	pane, _ = reg.Pane(next.PaneUID)
	if pane.Status.ProcessSession.StopRequested || !previous.StopRequested {
		t.Fatal("new generation inherited stop intent")
	}
	if err := m.RestoreProcessResume(&reg, next, *previous); err != nil {
		t.Fatal(err)
	}
	pane, _ = reg.Pane(previous.Binding.PaneUID)
	if !pane.Status.ProcessSession.StopRequested || pane.Status.ProcessSession.Binding != previous.Binding {
		t.Fatal("failed unspawned resume lost previous intent")
	}
	if reg.SchemaVersion != 5 {
		t.Fatalf("schema changed: %d", reg.SchemaVersion)
	}
}
