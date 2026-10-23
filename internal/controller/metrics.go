/*
Copyright 2026 Tobias Mende.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
)

const labelGroup = "group"

var (
	assignmentGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fip_announcer_assignment",
		Help: "1 for the node currently holding each address of a group.",
	}, []string{labelGroup, "ip", "node"})
	movesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "fip_announcer_moves_total",
		Help: "Floating IP moves of a group by reason.",
	}, []string{labelGroup, "reason"})
	lastMoveGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fip_announcer_last_move_timestamp_seconds",
		Help: "Unix time the Floating IPs of a group arrived on their current node.",
	}, []string{labelGroup})
	eligibleNodesGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fip_announcer_eligible_nodes",
		Help: "Number of nodes eligible to hold the Floating IPs of a group.",
	}, []string{labelGroup})
	degradedGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fip_announcer_group_degraded",
		Help: "1 when the group is Degraded.",
	}, []string{labelGroup})
)

var moveReasons = []v1alpha1.MoveReason{
	v1alpha1.MoveReasonInitial, v1alpha1.MoveReasonNodeCordoned, v1alpha1.MoveReasonProbeFailed,
	v1alpha1.MoveReasonEndpointsNotReady, v1alpha1.MoveReasonNodeNotReady, v1alpha1.MoveReasonServerGone,
	v1alpha1.MoveReasonExternal,
}

// initGroupMetrics creates the move counters of a group at 0, so rate() and
// increase() see the first move.
func initGroupMetrics(group string) {
	for _, reason := range moveReasons {
		movesTotal.WithLabelValues(group, string(reason))
	}
}

func init() {
	metrics.Registry.MustRegister(assignmentGauge, movesTotal, lastMoveGauge, eligibleNodesGauge, degradedGauge)
}

func forgetGroupMetrics(group string) {
	labels := prometheus.Labels{labelGroup: group}
	assignmentGauge.DeletePartialMatch(labels)
	movesTotal.DeletePartialMatch(labels)
	lastMoveGauge.DeletePartialMatch(labels)
	eligibleNodesGauge.DeletePartialMatch(labels)
	degradedGauge.DeletePartialMatch(labels)
}
