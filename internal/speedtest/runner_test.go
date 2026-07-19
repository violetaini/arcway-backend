package speedtest

import (
	"testing"
)

func TestSplitThreadByteBudgetPreservesTotalCap(t *testing.T) {
	const budget = int64(64*1024 + 3)
	var total int64
	for index := 0; index < 4; index++ {
		limit, active := splitThreadByteBudget(budget, 4, index)
		if !active {
			t.Fatalf("worker %d unexpectedly inactive", index)
		}
		total += limit
	}
	if total != budget {
		t.Fatalf("worker budgets sum to %d bytes, want %d", total, budget)
	}
}

func TestSplitThreadByteBudgetDoesNotTurnEmptyShareIntoUnlimited(t *testing.T) {
	for index := 0; index < 4; index++ {
		limit, active := splitThreadByteBudget(2, 4, index)
		if index < 2 && (!active || limit != 1) {
			t.Fatalf("worker %d = (%d, %v), want (1, true)", index, limit, active)
		}
		if index >= 2 && (active || limit != 0) {
			t.Fatalf("worker %d = (%d, %v), want (0, false)", index, limit, active)
		}
	}
}
