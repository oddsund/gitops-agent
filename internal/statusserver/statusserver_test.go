package statusserver

import (
	"errors"
	"testing"
	"time"
)

func TestTracker_HealthyByDefault(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	if !tr.Snapshot().Healthy() {
		t.Fatal("a fresh tracker with no completed cycle should be healthy")
	}
}

func TestTracker_CycleCompleteWithError(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.CycleComplete(errors.New("boom"))

	s := tr.Snapshot()
	if s.Healthy() {
		t.Fatal("Healthy() = true after a cycle failed")
	}
	if s.LastCycleError != "boom" {
		t.Errorf("LastCycleError = %q, want %q", s.LastCycleError, "boom")
	}
	if s.LastCycleAt.IsZero() {
		t.Error("LastCycleAt not set")
	}
}

func TestTracker_CycleCompleteRecoversFromError(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.CycleComplete(errors.New("boom"))
	tr.CycleComplete(nil)

	if !tr.Snapshot().Healthy() {
		t.Fatal("Healthy() = false after a later cycle succeeded")
	}
}

func TestTracker_SyncResult(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.SyncAttempt()
	tr.SyncResult("abc123", nil)

	s := tr.Snapshot()
	if s.CommitHash != "abc123" {
		t.Errorf("CommitHash = %q, want abc123", s.CommitHash)
	}
	if s.LastSyncAttempt.IsZero() || s.LastSyncSuccess.IsZero() {
		t.Error("LastSyncAttempt/LastSyncSuccess not set")
	}
	if s.LastSyncError != "" {
		t.Errorf("LastSyncError = %q, want empty", s.LastSyncError)
	}

	tr.SyncAttempt()
	tr.SyncResult("", errors.New("network down"))
	s = tr.Snapshot()
	if s.LastSyncError != "network down" {
		t.Errorf("LastSyncError = %q, want %q", s.LastSyncError, "network down")
	}
	// A failed sync must not clobber the last commit hash that did land.
	if s.CommitHash != "abc123" {
		t.Errorf("CommitHash = %q after failed sync, want it left at abc123", s.CommitHash)
	}
}

func TestTracker_ServiceLifecycle(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.SeenService("demoapp", "services/demoapp", true)
	tr.ServiceAttempt("demoapp")
	tr.ServiceResult("demoapp", nil)

	s := tr.Snapshot()
	if len(s.Services) != 1 {
		t.Fatalf("len(Services) = %d, want 1", len(s.Services))
	}
	svc := s.Services[0]
	if svc.Name != "demoapp" || svc.Path != "services/demoapp" || !svc.Enabled {
		t.Errorf("service = %+v", svc)
	}
	if !svc.LastAttemptOK {
		t.Error("LastAttemptOK = false after a successful deploy")
	}
	if svc.LastSuccess.IsZero() {
		t.Error("LastSuccess not set")
	}

	tr.ServiceAttempt("demoapp")
	tr.ServiceResult("demoapp", errors.New("compose up failed"))
	s = tr.Snapshot()
	svc = s.Services[0]
	if svc.LastAttemptOK {
		t.Error("LastAttemptOK = true after a failed deploy")
	}
	if svc.LastError != "compose up failed" {
		t.Errorf("LastError = %q", svc.LastError)
	}
	// The service's identity fields must survive a failed attempt.
	if svc.Path != "services/demoapp" {
		t.Errorf("Path = %q after failed deploy, want it preserved", svc.Path)
	}
}

func TestTracker_ServicesSortedByName(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	tr.SeenService("zeta", "services/zeta", true)
	tr.SeenService("alpha", "services/alpha", true)

	s := tr.Snapshot()
	if len(s.Services) != 2 || s.Services[0].Name != "alpha" || s.Services[1].Name != "zeta" {
		t.Errorf("Services = %+v, want alpha before zeta", s.Services)
	}
}

func TestTracker_SetNextCycle(t *testing.T) {
	tr := NewTracker("dev", time.Now())
	next := time.Now().Add(5 * time.Minute)
	tr.SetNextCycle(next, true)

	s := tr.Snapshot()
	if !s.NextCycleAt.Equal(next) {
		t.Errorf("NextCycleAt = %v, want %v", s.NextCycleAt, next)
	}
	if !s.Active {
		t.Error("Active = false, want true")
	}
}
