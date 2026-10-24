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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeletionPolicy decides what happens to Hetzner Floating IPs when the group is deleted.
// +kubebuilder:validation:Enum=Retain;Delete
type DeletionPolicy string

const (
	DeletionPolicyRetain DeletionPolicy = "Retain"
	DeletionPolicyDelete DeletionPolicy = "Delete"
)

// MoveReason records why the Floating IPs are on their current node.
// +kubebuilder:validation:Enum=Initial;NodeCordoned;ProbeFailed;EndpointsNotReady;NodeNotReady;ServerGone;External
type MoveReason string

const (
	MoveReasonInitial           MoveReason = "Initial"
	MoveReasonNodeCordoned      MoveReason = "NodeCordoned"
	MoveReasonProbeFailed       MoveReason = "ProbeFailed"
	MoveReasonEndpointsNotReady MoveReason = "EndpointsNotReady"
	MoveReasonNodeNotReady      MoveReason = "NodeNotReady"
	MoveReasonServerGone        MoveReason = "ServerGone"
	MoveReasonExternal          MoveReason = "External"
)

const (
	ConditionReady    = "Ready"
	ConditionDegraded = "Degraded"
)

// FloatingIPRef names a Hetzner Floating IP that is adopted if it exists and created otherwise.
type FloatingIPRef struct {
	// name is the Hetzner Floating IP name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// ipv6Address is the address taken from the /64, only used for IPv6.
	// +kubebuilder:default="::1"
	// +kubebuilder:validation:XValidation:rule="isIP(self) && ip(self).family() == 6",message="ipv6Address must be an IPv6 address"
	// +optional
	IPv6Address string `json:"ipv6Address,omitempty"`
}

// FloatingIPGroupSpec defines the desired state of FloatingIPGroup
// +kubebuilder:validation:XValidation:rule="has(self.ipv4) || has(self.ipv6)",message="at least one of ipv4 or ipv6 must be set"
type FloatingIPGroupSpec struct {
	// location is the Hetzner home location for created Floating IPs.
	// +kubebuilder:validation:MinLength=1
	Location string `json:"location"`

	// ipv4 is the IPv4 Floating IP of the group.
	// +optional
	IPv4 *FloatingIPRef `json:"ipv4,omitempty"`

	// ipv6 is the IPv6 Floating IP of the group.
	// +optional
	IPv6 *FloatingIPRef `json:"ipv6,omitempty"`

	// deletionPolicy decides whether the Floating IPs are kept or deleted with the group.
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// nodeSelector restricts eligible nodes, AND-combined with the member Services.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`
}

// PendingMove is a move of the Floating IPs that was started but not yet recorded.
type PendingMove struct {
	// node is the node the Floating IPs are being moved to.
	// +kubebuilder:validation:MinLength=1
	Node string `json:"node"`

	// reason is why the move was started.
	Reason MoveReason `json:"reason"`

	// startedAt is when the move was started.
	StartedAt metav1.Time `json:"startedAt"`
}

// FloatingIPGroupStatus defines the observed state of FloatingIPGroup.
type FloatingIPGroupStatus struct {
	// ipv4 is the assigned IPv4 address.
	// +optional
	IPv4 string `json:"ipv4,omitempty"`

	// ipv6 is the assigned IPv6 address taken from the /64.
	// +optional
	IPv6 string `json:"ipv6,omitempty"`

	// node is the node currently holding the Floating IPs.
	// +optional
	Node string `json:"node,omitempty"`

	// since is when the Floating IPs arrived on the node.
	// +optional
	Since *metav1.Time `json:"since,omitempty"`

	// reason is why the Floating IPs are on the node.
	// +optional
	Reason MoveReason `json:"reason,omitempty"`

	// pendingMove is a move that was started but not yet recorded, so another
	// leader can finish it with its real reason.
	// +optional
	PendingMove *PendingMove `json:"pendingMove,omitempty"`

	// conditions represent the current state of the FloatingIPGroup (Ready, Degraded).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="IPv4",type=string,JSONPath=`.status.ipv4`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.node`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63",message="name must be at most 63 characters; it becomes a Hetzner label value"

// FloatingIPGroup is the Schema for the floatingipgroups API
type FloatingIPGroup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FloatingIPGroup
	// +required
	Spec FloatingIPGroupSpec `json:"spec"`

	// status defines the observed state of FloatingIPGroup
	// +optional
	Status FloatingIPGroupStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FloatingIPGroupList contains a list of FloatingIPGroup
type FloatingIPGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FloatingIPGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &FloatingIPGroup{}, &FloatingIPGroupList{})
		return nil
	})
}
