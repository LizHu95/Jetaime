package memory

import "testing"

func TestInferenceAndSharingConsent(t *testing.T) {
	m := Memory{ID: "m", UserID: "a", Type: TypeConstraint, Content: "No peanuts", Strength: StrengthHard, Source: SourceExplicit}
	if !m.CanUseIn("personal", "a") || m.CanUseIn("couple", "") || m.CanUseIn("personal", "b") {
		t.Fatal("private memory crossed its owner boundary")
	}
	m.AllowedSpaceIDs = []string{"couple"}
	if !m.CanUseIn("couple", "") || m.CanUseIn("other", "") {
		t.Fatal("per-space consent not respected")
	}
	m.Source = SourceInferred
	if m.IsActive() || m.CanUseIn("couple", "") {
		t.Fatal("unconfirmed inference became effective")
	}
	m.Confirmed = true
	if !m.IsActive() {
		t.Fatal("confirmed inference did not become effective")
	}
}

func TestFeedbackCannotBecomeHardConstraint(t *testing.T) {
	m := Memory{ID: "m", UserID: "a", Type: TypeConstraint, Content: "Rejected once", Strength: StrengthHard, Source: SourceFeedback, Confirmed: true}
	if m.Validate() == nil || m.IsActive() {
		t.Fatal("raw feedback promoted to hard constraint")
	}
	m.Type, m.Strength = TypeFeedback, StrengthLow
	if !m.IsActive() {
		t.Fatal("raw feedback should remain available as weak evidence")
	}
}
