package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"miaomiaowux/internal/tunnelidentity"
)

type forwardingStorageFixture struct {
	repo    *TrafficRepository
	ctx     context.Context
	tunnel  *TunnelTemplate
	grant   *UserTunnelGrant
	servers []RemoteServer
}

func newForwardingStorageFixture(t *testing.T) forwardingStorageFixture {
	t.Helper()
	repo, err := NewTrafficRepository(filepath.Join(t.TempDir(), "forwarding.db"))
	if err != nil {
		t.Fatalf("NewTrafficRepository: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	for _, username := range []string{"admin", "alice", "bob"} {
		role := RoleUser
		if username == "admin" {
			role = RoleAdmin
		}
		if err := repo.CreateUser(ctx, username, username+"@example.test", username, "hash", role, ""); err != nil {
			t.Fatalf("CreateUser(%s): %v", username, err)
		}
	}
	servers := make([]RemoteServer, 2)
	for i := range servers {
		servers[i] = RemoteServer{Name: "edge-" + string(rune('a'+i)), Token: "token-" + string(rune('a'+i)), Status: RemoteServerStatusConnected, IPAddress: "203.0.113." + string(rune('1'+i)), XrayMode: "embedded"}
		if err := repo.CreateRemoteServer(ctx, &servers[i]); err != nil {
			t.Fatalf("CreateRemoteServer: %v", err)
		}
		if _, err := repo.UpdateRemoteServerXrayStatus(ctx, servers[i].ID, true, "test"); err != nil {
			t.Fatalf("UpdateRemoteServerXrayStatus: %v", err)
		}
	}
	tunnel, err := repo.CreateTunnelTemplate(ctx, TunnelTemplate{
		Name: "HK to US", State: TunnelStateActive, BillingMode: ManagedBillingBoth,
		TrafficMultiplierMilli: 2000, AllowManagedTarget: true, CreatedBy: "admin",
		Hops: []TunnelTemplateHop{{ServerID: servers[0].ID}, {ServerID: servers[1].ID}},
	})
	if err != nil {
		t.Fatalf("CreateTunnelTemplate: %v", err)
	}
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	grant, err := repo.CreateUserTunnelGrant(ctx, UserTunnelGrant{
		Username: "alice", TunnelID: tunnel.ID, Enabled: true, StartsAt: now.Add(-time.Hour),
		ExpiresAt: &expires, MaxActiveForwards: 100, BillingModeOverride: nil,
		AllowManagedTarget: true, CreatedBy: "admin",
	})
	if err != nil {
		t.Fatalf("CreateUserTunnelGrant: %v", err)
	}
	return forwardingStorageFixture{repo: repo, ctx: ctx, tunnel: tunnel, grant: grant, servers: servers}
}

func (f forwardingStorageFixture) createForward(t *testing.T, name string) *UserForwardRule {
	t.Helper()
	forward, err := f.repo.CreateUserForward(f.ctx, CreateUserForwardInput{
		Username: "alice", Name: name, GrantPublicID: f.grant.PublicID,
		TargetNodeID: 42, TargetHost: "198.51.100.42", TargetPort: 443,
		SourceCIDRs: []string{"198.51.100.0/24"}, EffectiveExpiresAt: f.grant.ExpiresAt,
		Actor: "alice",
	})
	if err != nil {
		t.Fatalf("CreateUserForward: %v", err)
	}
	return forward
}

func TestForwardingStorageOwnershipPortsAndIdentity(t *testing.T) {
	fixture := newForwardingStorageFixture(t)
	first := fixture.createForward(t, "first")
	second := fixture.createForward(t, "second")
	if len(first.Hops) != 2 || len(second.Hops) != 2 {
		t.Fatalf("unexpected hops: first=%d second=%d", len(first.Hops), len(second.Hops))
	}
	for i, hop := range first.Hops {
		if hop.ListenPort < 39000 || hop.ListenPort > 40000 {
			t.Fatalf("hop %d port=%d outside dedicated pool", i, hop.ListenPort)
		}
		if hop.ResourceID == "" || hop.ResourceTag != tunnelidentity.Tag(hop.ResourceID) {
			t.Fatalf("hop identity mismatch: %+v", hop)
		}
		if hop.ListenPort == second.Hops[i].ListenPort {
			t.Fatalf("server %d reused port %d", hop.ServerID, hop.ListenPort)
		}
	}
	if _, err := fixture.repo.GetUserForward(fixture.ctx, first.PublicID, "bob"); !errors.Is(err, ErrUserForwardNotFound) {
		t.Fatalf("cross-user lookup error=%v, want not found", err)
	}
	audits, err := fixture.repo.ListForwardAudit(fixture.ctx, "alice", "user_forward", first.ID, 10)
	if err != nil || len(audits) != 1 || audits[0].Action != "create" {
		t.Fatalf("unexpected audit: %+v err=%v", audits, err)
	}
}

func TestDeleteRemoteServerRejectsForwardingReferences(t *testing.T) {
	fixture := newForwardingStorageFixture(t)
	serverID := fixture.servers[0].ID
	if err := fixture.repo.DeleteRemoteServer(fixture.ctx, serverID); !errors.Is(err, ErrForwardingConflict) {
		t.Fatalf("DeleteRemoteServer error=%v, want forwarding conflict", err)
	}
	if server, err := fixture.repo.GetRemoteServer(fixture.ctx, serverID); err != nil || server == nil {
		t.Fatalf("referenced server was deleted: server=%v err=%v", server, err)
	}
}

func TestForwardingConcurrentPortAllocation(t *testing.T) {
	fixture := newForwardingStorageFixture(t)
	const count = 12
	ports := make(chan int, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			forward, err := fixture.repo.CreateUserForward(fixture.ctx, CreateUserForwardInput{
				Username: "alice", Name: "concurrent-" + time.Now().Add(time.Duration(i)).Format("150405.000000000"),
				GrantPublicID: fixture.grant.PublicID, TargetNodeID: int64(100 + i),
				TargetHost: "198.51.100.42", TargetPort: 443, Actor: "alice",
			})
			if err != nil {
				errs <- err
				return
			}
			ports <- forward.AllocatedEntryPort
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ports)
	for err := range errs {
		t.Errorf("concurrent CreateUserForward: %v", err)
	}
	seen := map[int]bool{}
	for port := range ports {
		if seen[port] {
			t.Fatalf("duplicate entry port %d", port)
		}
		seen[port] = true
	}
	if len(seen) != count {
		t.Fatalf("created=%d want=%d", len(seen), count)
	}
}

func TestForwardingGrantExpiryBumpsGenerationAndUsageBillsEntryOnce(t *testing.T) {
	fixture := newForwardingStorageFixture(t)
	forward := fixture.createForward(t, "metered")
	entry := forward.Hops[0]
	middle := forward.Hops[1]
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, entry.ServerID, entry.ResourceTag, "inbound", 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, entry.ServerID, entry.ResourceTag, "inbound", 100, 200, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, middle.ServerID, middle.ResourceTag, "inbound", 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, middle.ServerID, middle.ResourceTag, "inbound", 10_000, 20_000, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.SyncUserForwardUsage(fixture.ctx); err != nil {
		t.Fatalf("SyncUserForwardUsage: %v", err)
	}
	usage, err := fixture.repo.GetUserForwardUsage(fixture.ctx, forward.ID)
	if err != nil || usage.UplinkBytes != 100 || usage.DownlinkBytes != 200 {
		t.Fatalf("entry usage=%+v err=%v", usage, err)
	}
	grants, err := fixture.repo.ListUserTunnelGrants(fixture.ctx, "alice")
	if err != nil || len(grants) != 1 || grants[0].UsedBytes != 600 {
		t.Fatalf("billed grant=%+v err=%v", grants, err)
	}
	download := ManagedBillingDownload
	modeInput := *fixture.grant
	modeInput.BillingModeOverride = &download
	modeGrant, err := fixture.repo.UpdateUserTunnelGrant(fixture.ctx, fixture.grant.PublicID, "alice", modeInput, fixture.grant.Version, "admin")
	if err != nil {
		t.Fatalf("set download billing: %v", err)
	}
	second := fixture.createForward(t, "download-only")
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, second.Hops[0].ServerID, second.Hops[0].ResourceTag, "inbound", 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, second.Hops[0].ServerID, second.Hops[0].ResourceTag, "inbound", 100, 50, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.SyncUserForwardUsage(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	grants, err = fixture.repo.ListUserTunnelGrants(fixture.ctx, "alice")
	if err != nil || len(grants) != 1 || grants[0].UsedBytes != 700 {
		t.Fatalf("mixed both/download 2x billing=%+v err=%v", grants, err)
	}
	beforeExpiry, err := fixture.repo.GetUserForward(fixture.ctx, forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	newExpiry := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	updatedInput := *modeGrant
	updatedInput.ExpiresAt = &newExpiry
	updated, err := fixture.repo.UpdateUserTunnelGrant(fixture.ctx, fixture.grant.PublicID, "alice", updatedInput, modeGrant.Version, "admin")
	if err != nil {
		t.Fatalf("UpdateUserTunnelGrant: %v", err)
	}
	if updated.ExpiresAt == nil || !updated.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("grant expiry=%v want=%v", updated.ExpiresAt, newExpiry)
	}
	refreshed, err := fixture.repo.GetUserForward(fixture.ctx, forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Generation != beforeExpiry.Generation+1 || refreshed.Hops[0].Generation != beforeExpiry.Hops[0].Generation+1 {
		t.Fatalf("generation not bumped: forward=%d hop=%d", refreshed.Generation, refreshed.Hops[0].Generation)
	}
	if refreshed.EffectiveExpiresAt == nil || !refreshed.EffectiveExpiresAt.Equal(newExpiry) {
		t.Fatalf("effective expiry=%v want=%v", refreshed.EffectiveExpiresAt, newExpiry)
	}
}

func TestForwardingSeedsEntryTrafficBaselineBeforeFirstCollectorSample(t *testing.T) {
	fixture := newForwardingStorageFixture(t)
	forward := fixture.createForward(t, "first-sample")
	entry := forward.Hops[0]

	// This is deliberately the collector's first non-zero sample. Forward
	// creation must already have inserted the zero baseline for this tag.
	if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, entry.ServerID, entry.ResourceTag, "inbound", 100, 200, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repo.SyncUserForwardUsage(fixture.ctx); err != nil {
		t.Fatalf("SyncUserForwardUsage: %v", err)
	}
	usage, err := fixture.repo.GetUserForwardUsage(fixture.ctx, forward.ID)
	if err != nil || usage.UplinkBytes != 100 || usage.DownlinkBytes != 200 {
		t.Fatalf("first collector sample was not billed: usage=%+v err=%v", usage, err)
	}
	grants, err := fixture.repo.ListUserTunnelGrants(fixture.ctx, "alice")
	if err != nil || len(grants) != 1 || grants[0].UsedBytes != 600 {
		t.Fatalf("first collector sample grant billing=%+v err=%v", grants, err)
	}
}

func TestForwardingDeleteArchivesUsageAndRecreateCannotResetQuota(t *testing.T) {
	fixture := newForwardingStorageFixture(t)
	grantInput := *fixture.grant
	grantInput.TrafficLimitBytes = 1200
	grant, err := fixture.repo.UpdateUserTunnelGrant(fixture.ctx, fixture.grant.PublicID, "alice", grantInput, fixture.grant.Version, "admin")
	if err != nil {
		t.Fatalf("set traffic limit: %v", err)
	}

	recordAndDelete := func(name string) {
		t.Helper()
		forward := fixture.createForward(t, name)
		entry := forward.Hops[0]
		if err := fixture.repo.UpsertNodeTraffic(fixture.ctx, entry.ServerID, entry.ResourceTag, "inbound", 100, 200, false); err != nil {
			t.Fatal(err)
		}
		if err := fixture.repo.SyncUserForwardUsage(fixture.ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.repo.SetUserForwardDesired(fixture.ctx, forward.PublicID, "alice", ForwardDesiredDeleted, ForwardObservedCleanupPending, "none", "alice"); err != nil {
			t.Fatal(err)
		}
		if err := fixture.repo.FinalizeUserForwardDelete(fixture.ctx, forward.ID); err != nil {
			t.Fatal(err)
		}
		if err := fixture.repo.FinalizeUserForwardDelete(fixture.ctx, forward.ID); err != nil {
			t.Fatalf("repeated finalization must be idempotent: %v", err)
		}
	}

	recordAndDelete("first")
	grants, err := fixture.repo.ListUserTunnelGrants(fixture.ctx, "alice")
	if err != nil || len(grants) != 1 || grants[0].UsedBytes != 600 {
		t.Fatalf("archived usage after first delete=%+v err=%v", grants, err)
	}
	recordAndDelete("replacement")
	grants, err = fixture.repo.ListUserTunnelGrants(fixture.ctx, "alice")
	if err != nil || len(grants) != 1 || grants[0].UsedBytes != 1200 || grants[0].State != "over_limit" {
		t.Fatalf("archived usage after recreate=%+v err=%v", grants, err)
	}
	_, err = fixture.repo.CreateUserForward(fixture.ctx, CreateUserForwardInput{
		Username: "alice", Name: "quota-reset-attempt", GrantPublicID: grant.PublicID,
		TargetNodeID: 42, TargetHost: "198.51.100.42", TargetPort: 443, Actor: "alice",
	})
	if !errors.Is(err, ErrForwardingForbidden) {
		t.Fatalf("create after exhausted archived quota error=%v, want forbidden", err)
	}
}
