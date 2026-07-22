package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBackfillUserResetFromPackageRunsOnlyOnce(t *testing.T) {
	repo, err := NewTrafficRepository(filepath.Join(t.TempDir(), "user-reset-backfill.db"))
	if err != nil {
		t.Fatalf("NewTrafficRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	if err := repo.CreateUser(ctx, "alice", "alice@example.test", "Alice", "hash", RoleUser, ""); err != nil {
		t.Fatal(err)
	}
	packageID, err := repo.CreatePackage(ctx, Package{
		Name: "monthly", CycleDays: 30, IsReset: true, ResetDay: 8, Nodes: []int64{},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := repo.AssignPackageToUser(ctx, "alice", packageID, now, now.AddDate(0, 0, 30), false, 1); err != nil {
		t.Fatal(err)
	}

	n, alreadyDone, err := repo.BackfillUserResetFromPackage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyDone || n != 1 {
		t.Fatalf("first backfill n=%d alreadyDone=%v", n, alreadyDone)
	}
	user, err := repo.GetUser(ctx, "alice")
	if err != nil || !user.IsReset || user.ResetDay != 8 {
		t.Fatalf("first backfill user=%+v err=%v", user, err)
	}

	// This is now an intentional user-level override and must survive every restart.
	if err := repo.AssignPackageToUser(ctx, "alice", packageID, now, now.AddDate(0, 0, 60), false, 1); err != nil {
		t.Fatal(err)
	}
	n, alreadyDone, err = repo.BackfillUserResetFromPackage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !alreadyDone || n != 0 {
		t.Fatalf("second backfill n=%d alreadyDone=%v", n, alreadyDone)
	}
	user, err = repo.GetUser(ctx, "alice")
	if err != nil || user.IsReset {
		t.Fatalf("explicit reset override was not preserved: user=%+v err=%v", user, err)
	}
}
