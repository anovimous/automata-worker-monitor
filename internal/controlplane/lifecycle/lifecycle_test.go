package lifecycle

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAllowedActions(t *testing.T) {
	tests := []struct {
		lc       string
		cordoned bool
		running  bool
		want     []string
	}{
		{Provisioning, false, true, []string{}},
		{ProvisionFailed, false, true, []string{"remove"}},
		{Active, false, true, []string{"drain", "stop", "kill", "restart"}},
		{Draining, false, true, []string{"undrain", "stop", "kill"}},
		{Stopping, false, true, []string{"kill"}},
		{Stopped, false, false, []string{"start", "remove"}},
		{Stopped, true, false, []string{"remove"}},
		{Removing, false, false, []string{}},
		{Removed, false, false, []string{}},
		// a container that does not run can not be signalled or killed: a crashed worker is stopped or restarted
		{Active, false, false, []string{"stop", "restart"}},
		{Draining, false, false, []string{"stop"}},
		{Stopping, false, false, []string{}},
		{ProvisionFailed, false, false, []string{"remove"}},
	}
	for _, tt := range tests {
		if got := Allowed(tt.lc, tt.cordoned, tt.running); !slices.Equal(got, tt.want) {
			t.Errorf("Allowed(%s, cordoned %v, running %v) = %v, want %v", tt.lc, tt.cordoned, tt.running, got, tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	every := []string{ActionDrain, ActionUndrain, ActionStop, ActionKill, ActionRestart, ActionStart, ActionRemove}
	for _, lc := range []string{Provisioning, ProvisionFailed, Active, Draining, Stopping, Stopped, Removing, Removed} {
		for _, running := range []bool{true, false} {
			for _, action := range every {
				err := Check(lc, action, false, running)
				want := slices.Contains(Allowed(lc, false, running), action)
				if (err == nil) != want {
					t.Errorf("Check(%s, %s, running %v) = %v, want allowed %v", lc, action, running, err, want)
				}
				var te *TransitionError
				if err != nil && (!errors.As(err, &te) || !strings.Contains(err.Error(), "INVALID_TRANSITION")) {
					t.Errorf("Check(%s, %s) = %v, want a TransitionError", lc, action, err)
				}
			}
		}
	}
	if err := Check(Stopped, ActionStart, true, false); !errors.Is(err, ErrHostCordoned) {
		t.Errorf("start on a cordoned host = %v, want HOST_CORDONED", err)
	}
	err := Check(Active, ActionStart, false, true)
	var te *TransitionError
	if !errors.As(err, &te) || !slices.Equal(te.Allowed, []string{"drain", "stop", "kill", "restart"}) {
		t.Errorf("error = %v, want the allowed actions named", err)
	}
	if !strings.Contains((&TransitionError{Lifecycle: Removed, Action: ActionStop}).Error(), "allowed: none") {
		t.Error("an error without allowed actions does not say none")
	}
}

func TestAfterAction(t *testing.T) {
	want := map[string]string{ActionDrain: Draining, ActionUndrain: Active, ActionStop: Stopping, ActionKill: Stopping,
		ActionRestart: Active, ActionStart: Active, ActionRemove: Removing, "explode": ""}
	for action, lc := range want {
		if got := AfterAction(action); got != lc {
			t.Errorf("AfterAction(%s) = %s, want %s", action, got, lc)
		}
	}
}

var t0 = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func TestObserve(t *testing.T) {
	base := Observation{Now: t0.Add(time.Minute), LifecycleSince: t0, Running: true, TelemetryConfigured: true,
		RunningSince: t0, ProvisionTimeout: 2 * time.Minute}
	with := func(fn func(*Observation)) Observation {
		o := base
		fn(&o)
		return o
	}
	tests := []struct {
		name      string
		lc        string
		o         Observation
		next, why string
		changed   bool
	}{
		{"provisioned on the first status", Provisioning, with(func(o *Observation) { o.StatusOK = true }), Active, "", true},
		{"provisioning waits for a status", Provisioning, base, Provisioning, "", false},
		{"provisioned without telemetry after 30 s", Provisioning, with(func(o *Observation) { o.TelemetryConfigured = false }), Active, "", true},
		{"without telemetry, too early", Provisioning, with(func(o *Observation) { o.TelemetryConfigured = false; o.RunningSince = t0.Add(50 * time.Second) }), Provisioning, "", false},
		{"provision timeout", Provisioning, with(func(o *Observation) { o.Now = t0.Add(3 * time.Minute) }), ProvisionFailed, ReasonProvisionTimeout, true},
		{"a missing container while provisioning waits", Provisioning, with(func(o *Observation) { o.Missing, o.Running = true, false }), Provisioning, "", false},
		{"drained", Draining, with(func(o *Observation) { o.Running, o.Exited = false, true }), Stopped, ReasonDrained, true},
		{"a crash while draining stays", Draining, with(func(o *Observation) { o.Running, o.Exited, o.ExitCode = false, true, 1 }), Draining, "", false},
		{"stopped", Stopping, with(func(o *Observation) { o.Running, o.Exited, o.ExitCode = false, true, 137 }), Stopped, "", true},
		{"still stopping", Stopping, base, Stopping, "", false},
		{"exited cleanly by itself", Active, with(func(o *Observation) { o.Running, o.Exited = false, true }), Stopped, ReasonExitedCleanly, true},
		{"a crash keeps it active", Active, with(func(o *Observation) { o.Running, o.Exited, o.ExitCode = false, true, 1 }), Active, "", false},
		{"removed", Removing, with(func(o *Observation) { o.Missing = true }), Removed, "", true},
		{"still removing", Removing, base, Removing, "", false},
		{"disappeared", Active, with(func(o *Observation) { o.Missing = true }), Removed, ReasonDisappeared, true},
		{"a stopped one disappeared", Stopped, with(func(o *Observation) { o.Missing = true }), Removed, ReasonDisappeared, true},
		{"removed stays", Removed, with(func(o *Observation) { o.Missing = true }), Removed, "", false},
		{"failed stays", ProvisionFailed, with(func(o *Observation) { o.Missing = true }), ProvisionFailed, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, why, changed := Observe(tt.lc, tt.o)
			if next != tt.next || why != tt.why || changed != tt.changed {
				t.Errorf("Observe(%s) = %s, %q, %v; want %s, %q, %v", tt.lc, next, why, changed, tt.next, tt.why, tt.changed)
			}
		})
	}
}
