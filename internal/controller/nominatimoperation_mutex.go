/*
Copyright 2026.

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
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nominatimv1alpha1 "github.com/zebernst/nominatim-operator/api/v1alpha1"
)

const writePlaneRequeueDelay = 15 * time.Second

var errWritePlaneBusy = errors.New("write plane busy")

// writePlaneTerminalConflict marks a mutex conflict against a peer that already
// holds the write plane (Running or Job armed). The loser fails with Conflict.
type writePlaneTerminalConflict struct {
	peer *nominatimv1alpha1.NominatimOperation
}

func (e *writePlaneTerminalConflict) Error() string {
	return fmt.Sprintf("write plane held by %q", e.peer.Name)
}

// writePlaneDecision is the claim-path outcome of evaluateWritePlane.
type writePlaneDecision int

const (
	// writePlaneOK — claim may proceed (op wins creation race or no conflict).
	writePlaneOK writePlaneDecision = iota
	// writePlaneHold — a peer already holds the write plane; claim fails Conflict.
	writePlaneHold
	// writePlaneRaceWait — a creation-race peer wins by name order; claim requeues.
	writePlaneRaceWait
)

// writePlaneEval is the single peer-evaluation result for Operation claim and
// Nominatim schedule probes. Decision drives claim; ScheduleBusy drives schedule skip.
type writePlaneEval struct {
	Decision writePlaneDecision
	// Peer is set for Hold (the holder). May also be set for RaceWait (the winner).
	Peer *nominatimv1alpha1.NominatimOperation
	// BusyPeer is any active mutex-conflicting peer (holder or still racing).
	// Schedule probes skip whenever BusyPeer != nil — including when Decision is
	// OK because the synthetic probe name would win a creation race.
	BusyPeer *nominatimv1alpha1.NominatimOperation
}

// ScheduleBusy reports whether a parent should skip creating a scheduled Update.
func (e writePlaneEval) ScheduleBusy() bool {
	return e.BusyPeer != nil
}

// operationsMutexConflict reports whether two Operations may not run together
// per the write-heavy mutex policy (see nominatimoperation_resources.go).
func operationsMutexConflict(a, b *nominatimv1alpha1.NominatimOperation) bool {
	if a == nil || b == nil || a.Name == b.Name {
		return false
	}
	if a.Spec.NominatimRef.Name != b.Spec.NominatimRef.Name || a.Namespace != b.Namespace {
		return false
	}
	return isWriteHeavyOperation(a.Spec.Type) || isWriteHeavyOperation(b.Spec.Type)
}

// peerHoldsWritePlane reports whether a peer has passed the creation race and
// armed a Job (or is already Running). Empty/Pending-without-Job peers still
// participate in the name-order race instead of causing terminal Conflict.
func peerHoldsWritePlane(peer *nominatimv1alpha1.NominatimOperation) bool {
	if peer == nil || !isActiveOperationPhase(peer.Status.Phase) {
		return false
	}
	if peer.Status.Phase == nominatimv1alpha1.NominatimOperationPhaseRunning {
		return true
	}
	return peer.Status.JobRef != nil && peer.Status.JobRef.Name != ""
}

// evaluateWritePlane is the single write-plane peer evaluation for claim and schedule.
func evaluateWritePlane(op *nominatimv1alpha1.NominatimOperation, peers []nominatimv1alpha1.NominatimOperation) writePlaneEval {
	var ev writePlaneEval
	var raceWinner *nominatimv1alpha1.NominatimOperation

	for i := range peers {
		peer := &peers[i]
		if !operationsMutexConflict(op, peer) || !isActiveOperationPhase(peer.Status.Phase) {
			continue
		}
		if ev.BusyPeer == nil {
			ev.BusyPeer = peer
		}
		if peerHoldsWritePlane(peer) {
			if ev.Peer == nil || ev.Decision != writePlaneHold {
				ev.Decision = writePlaneHold
				ev.Peer = peer
			}
			continue
		}
		// Creation-race peer: lex-smaller name wins.
		if peer.Name < op.Name && raceWinner == nil {
			raceWinner = peer
		}
	}

	if ev.Decision == writePlaneHold {
		return ev
	}
	if raceWinner != nil {
		ev.Decision = writePlaneRaceWait
		ev.Peer = raceWinner
		return ev
	}
	ev.Decision = writePlaneOK
	return ev
}

func (r *NominatimOperationReconciler) listPeersForNominatim(ctx context.Context, op *nominatimv1alpha1.NominatimOperation) ([]nominatimv1alpha1.NominatimOperation, error) {
	list := &nominatimv1alpha1.NominatimOperationList{}
	if err := r.List(ctx, list, client.InNamespace(op.Namespace)); err != nil {
		return nil, err
	}
	out := make([]nominatimv1alpha1.NominatimOperation, 0, len(list.Items))
	for i := range list.Items {
		peer := &list.Items[i]
		if peer.Spec.NominatimRef.Name == op.Spec.NominatimRef.Name {
			out = append(out, *peer)
		}
	}
	return out, nil
}

// claimWritePlane registers op on the parent Nominatim's status.activeOperationRefs
// using a retry-on-conflict loop so only one creation-race winner arms a Job.
// Returns stop=true when Reconcile must return immediately (requeue or failed).
func (r *NominatimOperationReconciler) claimWritePlane(ctx context.Context, op *nominatimv1alpha1.NominatimOperation) (ctrl.Result, bool, error) {
	if op.Spec.NominatimRef.Name == "" {
		return ctrl.Result{}, false, nil
	}

	parentKey := types.NamespacedName{Name: op.Spec.NominatimRef.Name, Namespace: op.Namespace}
	ref := operationObjectReference(op)
	var terminal *writePlaneTerminalConflict

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		peers, err := r.listPeersForNominatim(ctx, op)
		if err != nil {
			return err
		}
		ev := evaluateWritePlane(op, peers)
		switch ev.Decision {
		case writePlaneHold:
			terminal = &writePlaneTerminalConflict{peer: ev.Peer}
			return nil
		case writePlaneRaceWait:
			return errWritePlaneBusy
		}

		parent := &nominatimv1alpha1.Nominatim{}
		if err := r.Get(ctx, parentKey, parent); err != nil {
			return client.IgnoreNotFound(err)
		}
		for _, existing := range parent.Status.ActiveOperationRefs {
			if sameOperationRef(existing, ref) {
				return nil
			}
		}
		parent.Status.ActiveOperationRefs = append(parent.Status.ActiveOperationRefs, ref)
		return r.Status().Update(ctx, parent)
	})

	if terminal != nil {
		return ctrl.Result{}, true, r.failOperation(ctx, op, reasonConflict, conflictMessage(terminal.peer))
	}
	if errors.Is(err, errWritePlaneBusy) {
		return ctrl.Result{RequeueAfter: writePlaneRequeueDelay}, true, nil
	}
	if err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, false, nil
}
