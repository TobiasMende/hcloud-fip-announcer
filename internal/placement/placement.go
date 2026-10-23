// Package placement decides which node should hold a group's Floating IPs.
package placement

import (
	"slices"
	"time"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

type Action string

const (
	Stay     Action = "Stay"
	Move     Action = "Move"
	Degraded Action = "Degraded"
)

type Input struct {
	// Holder is the node currently holding the group's FIPs, "" if none.
	Holder string
	// HolderReason is why the holder is ineligible; used as the move reason.
	HolderReason     v1alpha1.MoveReason
	HolderServerGone bool
	HolderCordoned   bool
	Eligible         []string
	LastMove         time.Time
	Now              time.Time
	AntiFlap         time.Duration
	LeaderNode       string
	// FIPCount is the number of group FIPs per node.
	FIPCount map[string]int
}

type Decision struct {
	Action Action
	Target string
	Reason v1alpha1.MoveReason
}

// Decide sticks to an eligible holder and otherwise picks the best eligible node.
func Decide(in Input) Decision {
	if in.Holder != "" && slices.Contains(in.Eligible, in.Holder) {
		return Decision{Action: Stay, Target: in.Holder}
	}
	if len(in.Eligible) == 0 {
		return Decision{Action: Degraded}
	}
	if in.Holder == "" {
		return Decision{Action: Move, Target: pick(in), Reason: v1alpha1.MoveReasonInitial}
	}
	urgent := in.HolderCordoned || in.HolderServerGone
	if !urgent && in.Now.Sub(in.LastMove) < in.AntiFlap {
		return Decision{Action: Stay, Target: in.Holder}
	}
	return Decision{Action: Move, Target: pick(in), Reason: in.HolderReason}
}

func pick(in Input) string {
	best := ""
	for _, n := range in.Eligible {
		if best == "" || better(in, n, best) {
			best = n
		}
	}
	return best
}

func better(in Input, a, b string) bool {
	if ca, cb := in.FIPCount[a], in.FIPCount[b]; ca != cb {
		return ca < cb
	}
	if la, lb := a == in.LeaderNode, b == in.LeaderNode; la != lb {
		return lb
	}
	return a < b
}
