package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"miaomiaowux/internal/storage"
)

type fakeForwardTunnelDeployer struct {
	mu                 sync.Mutex
	generation         map[string]int64
	state              map[string]string
	applyCalls         int
	suspendCalls       int
	removeCalls        int
	failApplyServer    int64
	failSuspendOnce    bool
	portConflictServer int64
	portConflictOnce   bool
	failRemoveResource string
	failRemoveOnce     bool
}

func newFakeForwardTunnelDeployer() *fakeForwardTunnelDeployer {
	return &fakeForwardTunnelDeployer{generation: map[string]int64{}, state: map[string]string{}}
}

func (d *fakeForwardTunnelDeployer) Probe(context.Context, int64) error { return nil }

func (d *fakeForwardTunnelDeployer) Apply(_ context.Context, spec ForwardTunnelSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyCalls++
	if d.portConflictOnce && d.portConflictServer == spec.ServerID {
		d.portConflictOnce = false
		return ErrForwardTunnelPortInUse
	}
	if d.failApplyServer == spec.ServerID {
		return errors.New("temporary apply failure")
	}
	current := d.generation[spec.ResourceID]
	if spec.Generation < current || (spec.Generation == current && d.state[spec.ResourceID] == "suspended") {
		return errors.New("generation conflict")
	}
	d.generation[spec.ResourceID] = spec.Generation
	d.state[spec.ResourceID] = "active"
	return nil
}

func (d *fakeForwardTunnelDeployer) Suspend(_ context.Context, _ int64, resourceID string, generation int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.suspendCalls++
	if d.failSuspendOnce {
		d.failSuspendOnce = false
		return errors.New("temporary suspend failure")
	}
	if generation < d.generation[resourceID] {
		return errors.New("stale generation")
	}
	d.generation[resourceID] = generation
	d.state[resourceID] = "suspended"
	return nil
}

func (d *fakeForwardTunnelDeployer) Remove(_ context.Context, _ int64, resourceID string, generation int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removeCalls++
	if d.failRemoveOnce && d.failRemoveResource == resourceID {
		d.failRemoveOnce = false
		return errors.New("temporary remove failure")
	}
	if generation < d.generation[resourceID] {
		return errors.New("stale generation")
	}
	d.generation[resourceID], d.state[resourceID] = generation, "deleted"
	return nil
}

type forwardingHandlerFixture struct {
	repo        *storage.TrafficRepository
	handler     *ForwardingHandler
	deployer    *fakeForwardTunnelDeployer
	grant       *storage.UserTunnelGrant
	forward     *storage.UserForwardRule
	nodeID      int64
	selectionID int64
}

func newForwardingHandlerFixture(t *testing.T) forwardingHandlerFixture {
	t.Helper()
	repo, err := storage.NewTrafficRepository(filepath.Join(t.TempDir(), "forwarding-handler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	ctx := context.Background()
	if err := repo.CreateUser(ctx, "admin", "admin@example.test", "Admin", "hash", storage.RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateUser(ctx, "alice", "alice@example.test", "Alice", "hash", storage.RoleUser, ""); err != nil {
		t.Fatal(err)
	}
	servers := make([]storage.RemoteServer, 2)
	for i := range servers {
		servers[i] = storage.RemoteServer{Name: []string{"entry", "target"}[i], Token: []string{"entry-token", "target-token"}[i], Status: storage.RemoteServerStatusConnected, IPAddress: []string{"203.0.113.10", "198.51.100.20"}[i], XrayMode: "embedded"}
		if err := repo.CreateRemoteServer(ctx, &servers[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.UpdateRemoteServerXrayStatus(ctx, servers[i].ID, true, "test"); err != nil {
			t.Fatal(err)
		}
	}
	node, err := repo.CreateNode(ctx, storage.Node{
		Username: "admin", NodeName: "Reality target", Protocol: "vless", Enabled: true,
		OriginalServer: servers[1].Name, InboundTag: "vless-reality",
		ClashConfig: `{"name":"Reality target","type":"vless","server":"198.51.100.20","port":443,"uuid":"admin-secret","servername":"www.example.com"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	offer, err := repo.CreateSelfServiceNodeOffer(ctx, node.ID, servers[1].ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	managedExpires := now.Add(2 * time.Hour)
	if _, err := repo.CreateUserServerGrant(ctx, storage.UserServerGrant{
		Username: "alice", ServerID: servers[1].ID, Enabled: true,
		StartsAt: now.Add(-time.Hour), ExpiresAt: &managedExpires, MaxActiveNodes: 1,
		BillingMode: storage.ManagedBillingDownload, ResetPolicy: storage.ManagedResetNone,
		ResetDay: 1, BillingTimezone: "Asia/Shanghai", CreatedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	activation, err := repo.ActivateUserNodeSelection(ctx, "alice", offer.ID, "alice", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkUserInboundAccessSourceApplied(ctx, activation.Source.ID, activation.Source.Generation,
		storage.ManagedObservedActive, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveUserInboundConfig(ctx, storage.UserInboundConfig{Username: "alice", ServerID: servers[1].ID, InboundTag: node.InboundTag, Protocol: "vless", CredentialJSON: `{"id":"alice-user-id"}`}); err != nil {
		t.Fatal(err)
	}
	tunnel, err := repo.CreateTunnelTemplate(ctx, storage.TunnelTemplate{Name: "two hop", State: storage.TunnelStateActive, BillingMode: storage.ManagedBillingDownload, TrafficMultiplierMilli: 1000, CreatedBy: "admin", Hops: []storage.TunnelTemplateHop{{ServerID: servers[0].ID}, {ServerID: servers[1].ID}}})
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Hour)
	grant, err := repo.CreateUserTunnelGrant(ctx, storage.UserTunnelGrant{Username: "alice", TunnelID: tunnel.ID, Enabled: true, StartsAt: now.Add(-time.Hour), ExpiresAt: &expires, MaxActiveForwards: 4, AllowManagedTarget: true, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := repo.CreateUserForward(ctx, storage.CreateUserForwardInput{Username: "alice", Name: "reality", GrantPublicID: grant.PublicID, TargetNodeID: node.ID, TargetHost: servers[1].IPAddress, TargetPort: 443, EffectiveExpiresAt: &expires, Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	deployer := newFakeForwardTunnelDeployer()
	handler := NewForwardingHandler(repo, deployer)
	if err := handler.deployForward(ctx, forward); err != nil {
		t.Fatal(err)
	}
	forward, err = repo.GetUserForward(ctx, forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	return forwardingHandlerFixture{repo: repo, handler: handler, deployer: deployer, grant: grant, forward: forward, nodeID: node.ID, selectionID: activation.Selection.ID}
}

func TestForwardingReconcileGenerationSuspendResumeAndHealthyRenew(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	ctx := context.Background()
	initialGeneration := fixture.forward.Generation
	fixture.handler.reconcileForwards(ctx)
	renewed, err := fixture.repo.GetUserForward(ctx, fixture.forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Generation != initialGeneration {
		t.Fatalf("healthy renewal bumped generation: got=%d want=%d", renewed.Generation, initialGeneration)
	}
	disabled := *fixture.grant
	disabled.Enabled = false
	disabledGrant, err := fixture.repo.UpdateUserTunnelGrant(ctx, fixture.grant.PublicID, "alice", disabled, fixture.grant.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler.reconcileForwards(ctx)
	suspended, err := fixture.repo.GetUserForward(ctx, fixture.forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if suspended.DesiredState != storage.ForwardDesiredActive || suspended.ObservedState != storage.ForwardObservedSuspended || suspended.Generation <= initialGeneration {
		t.Fatalf("unexpected system suspension: %+v", suspended)
	}
	suspendGeneration := suspended.Generation
	enabled := *disabledGrant
	enabled.Enabled = true
	newExpiry := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	enabled.ExpiresAt = &newExpiry
	if _, err := fixture.repo.UpdateUserTunnelGrant(ctx, fixture.grant.PublicID, "alice", enabled, disabledGrant.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	fixture.handler.reconcileForwards(ctx)
	resumed, err := fixture.repo.GetUserForward(ctx, fixture.forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ObservedState != storage.ForwardObservedActive || resumed.Generation <= suspendGeneration {
		t.Fatalf("forward did not resume with a newer generation: %+v", resumed)
	}
	if resumed.EffectiveExpiresAt == nil || !resumed.EffectiveExpiresAt.Equal(newExpiry) {
		t.Fatalf("effective expiry=%v want=%v", resumed.EffectiveExpiresAt, newExpiry)
	}
	stableGeneration := resumed.Generation
	fixture.handler.reconcileForwards(ctx)
	stable, _ := fixture.repo.GetUserForward(ctx, fixture.forward.PublicID, "alice")
	if stable.Generation != stableGeneration {
		t.Fatalf("second healthy renewal bumped generation: %d -> %d", stableGeneration, stable.Generation)
	}
}

func TestForwardingHealthyRenewFailureDoesNotSuspendExistingHops(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	beforeSuspends := fixture.deployer.suspendCalls
	fixture.deployer.failApplyServer = fixture.forward.Hops[0].ServerID
	if err := fixture.handler.deployForward(context.Background(), fixture.forward); err == nil {
		t.Fatal("expected renewal failure")
	}
	if fixture.deployer.suspendCalls != beforeSuspends {
		t.Fatalf("healthy renewal failure suspended existing hop: before=%d after=%d", beforeSuspends, fixture.deployer.suspendCalls)
	}
	latest, err := fixture.repo.GetUserForward(context.Background(), fixture.forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ObservedState != storage.ForwardObservedActive || fixture.handler.userForwardDTO(context.Background(), *latest).EntryHost == "" {
		t.Fatalf("healthy lease was hidden after renewal failure: %+v", latest)
	}
}

func TestForwardingInactiveProvisioningRetriesSameGeneration(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	fixture.deployer.failSuspendOnce = true
	if err := fixture.handler.suspendForward(context.Background(), fixture.forward, "alice"); err == nil {
		t.Fatal("expected first suspend failure")
	}
	pending, err := fixture.repo.GetUserForward(context.Background(), fixture.forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	generation := pending.Generation
	if pending.DesiredState != storage.ForwardDesiredInactive {
		t.Fatalf("desired=%s", pending.DesiredState)
	}
	fixture.handler.reconcileForwards(context.Background())
	suspended, err := fixture.repo.GetUserForward(context.Background(), fixture.forward.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if suspended.ObservedState != storage.ForwardObservedSuspended || suspended.Generation != generation {
		t.Fatalf("inactive retry changed generation or failed: %+v", suspended)
	}
}

func TestForwardingPortConflictReallocatesIdentityAndRetries(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	ctx := context.Background()
	second, err := fixture.repo.CreateUserForward(ctx, storage.CreateUserForwardInput{
		Username: "alice", Name: "port retry", GrantPublicID: fixture.grant.PublicID,
		TargetNodeID: fixture.forward.TargetNodeID, TargetHost: fixture.forward.TargetHost,
		TargetPort: fixture.forward.TargetPort, EffectiveExpiresAt: fixture.grant.ExpiresAt, Actor: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldEntryPort := second.AllocatedEntryPort
	oldResourceIDs := make([]string, len(second.Hops))
	oldGenerations := make([]int64, len(second.Hops))
	for i := range second.Hops {
		oldResourceIDs[i], oldGenerations[i] = second.Hops[i].ResourceID, second.Hops[i].Generation
	}
	fixture.deployer.portConflictServer = second.Hops[0].ServerID
	fixture.deployer.portConflictOnce = true
	fixture.deployer.failRemoveResource = second.Hops[1].ResourceID
	fixture.deployer.failRemoveOnce = true
	if err := fixture.handler.deployForward(ctx, second); err != nil {
		t.Fatalf("deploy after port reallocation: %v", err)
	}
	updated, err := fixture.repo.GetUserForward(ctx, second.PublicID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ObservedState != storage.ForwardObservedActive || updated.AllocatedEntryPort == oldEntryPort {
		t.Fatalf("port conflict did not replace entry port: before=%d after=%d state=%s", oldEntryPort, updated.AllocatedEntryPort, updated.ObservedState)
	}
	for i := range updated.Hops {
		if updated.Hops[i].ResourceID == oldResourceIDs[i] || updated.Hops[i].Generation <= oldGenerations[i] {
			t.Fatalf("hop %d identity was not durably replaced: before=%s/%d after=%s/%d", i,
				oldResourceIDs[i], oldGenerations[i], updated.Hops[i].ResourceID, updated.Hops[i].Generation)
		}
	}
	if fixture.deployer.removeCalls < len(second.Hops)+1 {
		t.Fatalf("partial cleanup was not retried: remove calls=%d", fixture.deployer.removeCalls)
	}
}

func TestForwardClientConfigUsesUserCredentialView(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/user/forwards/"+fixture.forward.PublicID+"/client-config", nil)
	fixture.handler.writeForwardClientConfig(response, request, fixture.forward, "alice")
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "admin-secret") || !strings.Contains(body, "alice-user-id") {
		t.Fatalf("client config credential isolation failed: %s", body)
	}
}

func TestResolveManagedForwardTargetRejectsPackageOnlyVisibility(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	ctx := context.Background()
	result, err := fixture.repo.DeactivateUserNodeSelection(ctx, "alice", fixture.selectionID, "alice",
		storage.ManagedSuspendUserDisabled, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repo.MarkUserInboundAccessSourceApplied(ctx, result.Source.ID, result.Source.Generation,
		storage.ManagedObservedInactive, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	packageID, err := fixture.repo.CreatePackage(ctx, storage.Package{
		Name: "legacy visibility", CycleDays: 30, ResetDay: 1,
		Nodes: []int64{fixture.nodeID}, TrafficMode: "oneway",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := fixture.repo.AssignPackageToUser(ctx, "alice", packageID, now.Add(-time.Hour), now.Add(time.Hour), false, 1); err != nil {
		t.Fatal(err)
	}
	visible, err := collectUserVisibleNodes(ctx, fixture.repo, "alice")
	if err != nil || len(visible) == 0 {
		t.Fatalf("package node is not visible for test setup: visible=%v err=%v", visible, err)
	}
	if _, _, _, _, err := fixture.handler.resolveManagedForwardTarget(ctx, "alice", fixture.nodeID); !errors.Is(err, storage.ErrForwardingForbidden) {
		t.Fatalf("package-only target error=%v, want forbidden", err)
	}
}

func TestTunnelSpecNormalizesPreviousHopHostCIDRs(t *testing.T) {
	fixture := newForwardingHandlerFixture(t)
	ctx := context.Background()
	if _, _, err := fixture.repo.UpdateRemoteServerHeartbeat(ctx, "entry-token", "::ffff:192.0.2.9", "2001:db8::9"); err != nil {
		t.Fatal(err)
	}
	spec, err := fixture.handler.tunnelSpec(ctx, fixture.forward, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"192.0.2.9/32": true, "2001:db8::9/128": true}
	if len(spec.SourceCIDRs) != len(want) {
		t.Fatalf("source CIDRs=%v", spec.SourceCIDRs)
	}
	for _, cidr := range spec.SourceCIDRs {
		if !want[cidr] {
			t.Fatalf("unexpected normalized CIDR %q in %v", cidr, spec.SourceCIDRs)
		}
	}
}

func TestProbeTunnelServerCapabilitiesRejectsIPv6OnlyServer(t *testing.T) {
	repo, err := storage.NewTrafficRepository(filepath.Join(t.TempDir(), "forwarding-ipv6.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	server := storage.RemoteServer{
		Name: "ipv6-only", Token: "ipv6-token", Status: storage.RemoteServerStatusConnected,
		IPAddressV6: "2001:db8::10", XrayMode: "embedded",
	}
	if err := repo.CreateRemoteServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateRemoteServerXrayStatus(context.Background(), server.ID, true, "test"); err != nil {
		t.Fatal(err)
	}
	handler := NewForwardingHandler(repo, newFakeForwardTunnelDeployer())
	if err := handler.probeTunnelServerCapabilities(context.Background(), []int64{server.ID}); !errors.Is(err, ErrForwardTunnelCapability) {
		t.Fatalf("IPv6-only capability error=%v", err)
	}
}

func TestDecodeForwardingJSONRejectsTrailingValue(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"one"} {"name":"two"}`))
	var body struct {
		Name string `json:"name"`
	}
	if decodeForwardingJSON(response, request, &body) {
		t.Fatal("accepted a second JSON value")
	}
	if response.Code != 400 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNormalizeForwardSourceCIDRsRejectsBroadMappedPrefix(t *testing.T) {
	if _, err := normalizeForwardSourceCIDRs([]string{"::ffff:192.0.2.1/64"}); !errors.Is(err, storage.ErrForwardingInvalid) {
		t.Fatalf("mapped /64 error=%v", err)
	}
	values, err := normalizeForwardSourceCIDRs([]string{"198.51.100.7", "198.51.100.7/32"})
	if err != nil || len(values) != 1 || values[0] != "198.51.100.7/32" {
		t.Fatalf("normalized=%v err=%v", values, err)
	}
}
