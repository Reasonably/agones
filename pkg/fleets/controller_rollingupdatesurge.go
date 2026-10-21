// Copyright Contributors to Agones a Series of LF Projects, LLC.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fleets

import (
	agonesv1 "agones.dev/agones/pkg/apis/agones/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// rollingUpdateActiveSurge returns the replica count for the active GameServerSet when the
// RollingUpdateSurge feature is enabled. base is the replica count computed without the feature.
//
// It follows the Kubernetes Deployment definition of maxSurge: the active GameServerSet may grow
// until all GameServerSets together hold fleet.Spec.Replicas + maxSurge GameServers, and never
// beyond fleet.Spec.Replicas on its own. Without this, a Fleet whose inactive GameServerSets are
// mostly Allocated starts the new GameServerSet with only as many GameServers as the inactive
// ones have unallocated, so very few new GameServers become Ready and the Ready based scale down
// of the inactive GameServerSets cannot proceed while the Fleet is growing.
//
// base can keep or lower the current target, e.g. when the Fleet is scaled down, but only the
// surge budget raises it, so a target already above the budget is kept rather than cut.
//
// It is also applied once the inactive GameServerSets have been scaled to zero. Falling back to
// base at that point would delete the GameServers started by the surge in one step, Ready ones
// included; instead they are released as the Fleet replicas decrease.
func (c *Controller) rollingUpdateActiveSurge(fleet *agonesv1.Fleet, active *agonesv1.GameServerSet, rest []*agonesv1.GameServerSet, base int32) (int32, error) {
	if fleet.Spec.Replicas == 0 {
		return base, nil
	}

	// while the active GameServerSet is still creating GameServers, hold its current target so
	// the GameServers already requested are neither removed nor topped up past the budget.
	// Status above Spec is not waited on: Allocated GameServers can keep it there indefinitely.
	if active.Spec.Replicas > active.Status.Replicas {
		return fleet.UpperBoundReplicas(active.Spec.Replicas), nil
	}

	r, err := intstr.GetValueFromIntOrPercent(fleet.Spec.Strategy.RollingUpdate.MaxSurge, int(fleet.Spec.Replicas), true)
	if err != nil {
		return 0, c.errs.Wrapf(err, "error parsing MaxSurge value: %s", fleet.ObjectMeta.Name)
	}
	surge := int32(r)

	// count the GameServers an inactive GameServerSet is still creating (Spec above Status) as well
	// as those it cannot remove because they are Allocated (Status above Spec).
	restReplicas := agonesv1.SumGameServerSets(rest, func(gsSet *agonesv1.GameServerSet) int32 {
		return max(gsSet.Spec.Replicas, gsSet.Status.Replicas)
	})

	surged := fleet.LowerBoundReplicas(fleet.UpperBoundReplicas(fleet.Spec.Replicas + surge - restReplicas))
	replicas := max(min(base, active.Spec.Replicas), surged)

	loggerForFleet(fleet, c.baseLogger).WithField("gameserverset", active.ObjectMeta.Name).WithField("replicas", replicas).
		WithField("surge", surge).WithField("restReplicas", restReplicas).
		Debug("applying rolling update surge to active gameserverset")

	return replicas, nil
}
