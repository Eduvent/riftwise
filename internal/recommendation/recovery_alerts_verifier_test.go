package recommendation

import (
	"reflect"
	"testing"

	"lolengine/internal/domain"
	"lolengine/internal/itemization"
)

func TestCoordinatorRecoveryAlertsRearmsAfterFreshInsufficiency(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{
			false: "continuous",
			true:  "restart-mid-stale",
		}[restart], func(t *testing.T) {
			flow := newRecoveryAlertFlow(t)
			flow.fresh(900, 4401, 1057, itemization.StatusReady)
			flow.stale()
			if restart {
				flow.restart()
			}

			// A real fresh insufficient observation must clear the remembered
			// affordability, rather than suppress the next genuine crossing.
			flow.fresh(500, 4401, 1057, itemization.StatusSaveForComponent)
			want := []domain.DecisionKind{
				domain.DecisionComponentAffordable,
				domain.DecisionDataStale,
				domain.DecisionRulesUnavailable,
				domain.DecisionConnectionRestored,
			}
			before := flow.assertHistory(want, true)

			flow.fresh(900, 4401, 1057, itemization.StatusReady)
			want = append(want, domain.DecisionComponentAffordable)
			after := flow.assertHistory(want, true)
			if !reflect.DeepEqual(after[:len(before)], before) {
				t.Fatal("new genuine crossing rewrote durable history")
			}

			flow.fresh(901, 4401, 1057, itemization.StatusReady)
			if !reflect.DeepEqual(flow.assertHistory(want, true), after) {
				t.Fatal("new genuine crossing repeated")
			}
		})
	}
}
