package placement

import (
	"testing"
	"time"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func base() Input {
	return Input{
		Holder:   "a",
		Eligible: []string{"a", "b"},
		LastMove: now.Add(-time.Hour),
		Now:      now,
		AntiFlap: 30 * time.Second,
	}
}

func TestStayWhenHolderEligible(t *testing.T) {
	in := base()
	in.LastMove = now
	if got := Decide(in); got.Action != Stay || got.Target != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestNoFailbackWhenOriginalReturns(t *testing.T) {
	in := base()
	in.Holder = "b"
	in.Eligible = []string{"a", "b"}
	if got := Decide(in); got.Action != Stay {
		t.Fatalf("got %+v", got)
	}
}

func TestMoveOnCordonSkipsAntiFlap(t *testing.T) {
	in := base()
	in.Eligible = []string{"b"}
	in.HolderCordoned = true
	in.HolderReason = v1alpha1.MoveReasonNodeCordoned
	in.LastMove = now.Add(-time.Second)
	want := Decision{Action: Move, Target: "b", Reason: v1alpha1.MoveReasonNodeCordoned}
	if got := Decide(in); got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestMoveOnServerGoneSkipsAntiFlap(t *testing.T) {
	in := base()
	in.Eligible = []string{"b"}
	in.HolderServerGone = true
	in.HolderReason = v1alpha1.MoveReasonServerGone
	in.LastMove = now.Add(-time.Second)
	want := Decision{Action: Move, Target: "b", Reason: v1alpha1.MoveReasonServerGone}
	if got := Decide(in); got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestAntiFlapDelaysOtherMoves(t *testing.T) {
	in := base()
	in.Eligible = []string{"b"}
	in.HolderReason = v1alpha1.MoveReasonProbeFailed
	in.LastMove = now.Add(-time.Second)
	if got := Decide(in); got.Action != Stay || got.Target != "a" {
		t.Fatalf("got %+v", got)
	}
	in.LastMove = now.Add(-time.Minute)
	want := Decision{Action: Move, Target: "b", Reason: v1alpha1.MoveReasonProbeFailed}
	if got := Decide(in); got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestDegradedWhenNoEligible(t *testing.T) {
	in := base()
	in.Eligible = nil
	in.HolderReason = v1alpha1.MoveReasonNodeNotReady
	if got := Decide(in); got.Action != Degraded || got.Target != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestPrefersNotLeaderNode(t *testing.T) {
	in := base()
	in.Eligible = []string{"b", "c", "d"}
	in.HolderReason = v1alpha1.MoveReasonProbeFailed
	in.LeaderNode = "b"
	in.FIPCount = map[string]int{"d": 1}
	if got := Decide(in); got.Target != "c" {
		t.Fatalf("got %+v", got)
	}
	in.FIPCount = map[string]int{"b": 1, "c": 1, "d": 1}
	if got := Decide(in); got.Target != "c" {
		t.Fatalf("leader must lose the tie, got %+v", got)
	}
	in.FIPCount = map[string]int{"b": 0, "c": 2, "d": 2}
	if got := Decide(in); got.Target != "b" {
		t.Fatalf("fewest FIPs first, got %+v", got)
	}
}

func TestInitialAssignment(t *testing.T) {
	in := base()
	in.Holder = ""
	in.LastMove = now
	want := Decision{Action: Move, Target: "a", Reason: v1alpha1.MoveReasonInitial}
	if got := Decide(in); got != want {
		t.Fatalf("got %+v", got)
	}
}

func TestPrefersHomeLocation(t *testing.T) {
	in := base()
	in.Holder = "a"
	in.HolderCordoned = true
	in.HolderReason = v1alpha1.MoveReasonNodeCordoned
	in.Eligible = []string{"b", "c"}
	in.HomeNodes = map[string]bool{"c": true}
	in.FIPCount = map[string]int{"c": 1}
	if got := Decide(in); got.Action != Move || got.Target != "c" {
		t.Fatalf("home location must win over fewer FIPs and name order, got %+v", got)
	}
}
