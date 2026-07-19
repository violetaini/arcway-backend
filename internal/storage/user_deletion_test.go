package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProvisioningLeaseSerializesUserDeletion(t *testing.T) {
	repo, _ := newManagedNodesTestRepository(t)
	ctx, server, _, _ := seedManagedNodesTest(t, repo)
	if err := repo.SaveUserInboundConfig(ctx, UserInboundConfig{
		Username: "alice", ServerID: server.ID, InboundTag: "lease-in", Protocol: "vless",
		CredentialJSON: `{"id":"lease-uuid","email":"alice__lease-in"}`,
	}); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	provisionDone := make(chan error, 1)
	go func() {
		provisionDone <- repo.WithUserProvisioningLease(context.Background(), "alice", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	type deletionResult struct {
		sources []UserInboundAccessSource
		err     error
	}
	deletionDone := make(chan deletionResult, 1)
	go func() {
		sources, err := repo.PrepareUserDeletion(context.Background(), "alice", "admin")
		deletionDone <- deletionResult{sources: sources, err: err}
	}()
	select {
	case result := <-deletionDone:
		t.Fatalf("deletion bypassed active provisioning lease: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-provisionDone; err != nil {
		t.Fatal(err)
	}
	result := <-deletionDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.sources) == 0 {
		t.Fatal("deletion did not retain credential revocation material")
	}
	if err := repo.WithUserProvisioningLease(ctx, "alice", func() error {
		t.Fatal("provision callback ran after tombstone commit")
		return nil
	}); !errors.Is(err, ErrUserDeletionPending) {
		t.Fatalf("post-tombstone lease error = %v", err)
	}
}

func TestRemoteServerGuardSecretIsStableAndSeparate(t *testing.T) {
	repo, _ := newManagedNodesTestRepository(t)
	ctx, server, _, _ := seedManagedNodesTest(t, repo)
	first, err := repo.GetOrCreateRemoteServerGuardSecret(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.GetOrCreateRemoteServerGuardSecret(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("guard secret was not stable: %q vs %q", first, second)
	}
	if first == server.Token {
		t.Fatal("guard secret reused rotating server token")
	}
}

func TestFinalizeUserDeletionCascadesManagedAuthorizationAndTokens(t *testing.T) {
	repo, _ := newManagedNodesTestRepository(t)
	ctx, server, _, offer := seedManagedNodesTest(t, repo)
	now := time.Now().UTC()
	createManagedGrantForTest(t, repo, ctx, server.ID, now)
	activation, err := repo.ActivateUserNodeSelection(ctx, "alice", offer.ID, "alice", now)
	if err != nil {
		t.Fatalf("ActivateUserNodeSelection: %v", err)
	}
	if err := repo.SaveUserInboundConfig(ctx, UserInboundConfig{
		Username: "alice", ServerID: server.ID, InboundTag: offer.InboundTag, Protocol: "vless",
		CredentialJSON: `{"id":"alice-delete-uuid","email":"alice__vless-in"}`,
	}); err != nil {
		t.Fatalf("SaveUserInboundConfig: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO user_api_tokens
    (username, name, token_hash) VALUES (?, ?, ?)`, "alice", "delete-test", "delete-test-hash"); err != nil {
		t.Fatalf("insert API token: %v", err)
	}
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO user_outbounds
    (username, server_id, inbound_tag, outbound_tag, outbound_json) VALUES (?, ?, ?, ?, ?)`,
		"alice", server.ID, offer.InboundTag, "alice-out", `{}`); err != nil {
		t.Fatalf("insert user outbound: %v", err)
	}

	sources, err := repo.PrepareUserDeletion(ctx, "alice", "admin")
	if err != nil {
		t.Fatalf("PrepareUserDeletion: %v", err)
	}
	if len(sources) < 2 {
		t.Fatalf("credential and selection sources were not retained: %#v", sources)
	}
	user, err := repo.GetUser(ctx, "alice")
	if err != nil || user.IsActive {
		t.Fatalf("prepared user was not disabled: user=%#v err=%v", user, err)
	}
	selection, err := repo.GetUserNodeSelection(ctx, activation.Selection.ID)
	if err != nil || selection.DesiredEnabled {
		t.Fatalf("prepared selection remained enabled: selection=%#v err=%v", selection, err)
	}
	ready, err := repo.IsUserDeletionReady(ctx, "alice")
	if err != nil || ready {
		t.Fatalf("unapplied revocations reported ready: ready=%v err=%v", ready, err)
	}
	if err := repo.FinalizeUserDeletion(ctx, "alice", "admin"); !errors.Is(err, ErrUserDeletionPending) {
		t.Fatalf("unsafe finalization error=%v want=%v", err, ErrUserDeletionPending)
	}
	configs, err := repo.GetUserInboundConfigs(ctx, "alice")
	if err != nil || len(configs) != 1 {
		t.Fatalf("unsafe finalization removed credential: configs=%#v err=%v", configs, err)
	}

	for _, source := range sources {
		if _, err := repo.MarkUserInboundAccessSourceApplied(ctx, source.ID, source.Generation,
			ManagedObservedInactive, now.Add(time.Second)); err != nil {
			t.Fatalf("MarkUserInboundAccessSourceApplied(%d): %v", source.ID, err)
		}
	}
	ready, err = repo.IsUserDeletionReady(ctx, "alice")
	if err != nil || !ready {
		t.Fatalf("applied revocations not ready: ready=%v err=%v", ready, err)
	}
	if err := repo.FinalizeUserDeletion(ctx, "alice", "admin"); err != nil {
		t.Fatalf("FinalizeUserDeletion: %v", err)
	}

	if _, err := repo.GetUser(ctx, "alice"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("user survived finalization: %v", err)
	}
	for _, check := range []struct {
		name  string
		query string
	}{
		{name: "grants", query: `SELECT COUNT(*) FROM user_server_grants WHERE username = 'alice'`},
		{name: "selections", query: `SELECT COUNT(*) FROM user_node_selections WHERE grant_id = ` + "?"},
		{name: "sources", query: `SELECT COUNT(*) FROM user_inbound_access_sources WHERE username = 'alice'`},
		{name: "usage", query: `SELECT COUNT(*) FROM user_node_selection_usage WHERE selection_id = ` + "?"},
		{name: "credentials", query: `SELECT COUNT(*) FROM user_inbound_configs WHERE username = 'alice'`},
		{name: "API tokens", query: `SELECT COUNT(*) FROM user_api_tokens WHERE username = 'alice'`},
		{name: "outbounds", query: `SELECT COUNT(*) FROM user_outbounds WHERE username = 'alice'`},
		{name: "tombstone", query: `SELECT COUNT(*) FROM user_deletion_tombstones WHERE username = 'alice'`},
	} {
		var count int
		args := []any{}
		if check.name == "selections" {
			args = append(args, activation.Selection.GrantID)
		} else if check.name == "usage" {
			args = append(args, activation.Selection.ID)
		}
		if err := repo.db.QueryRowContext(ctx, check.query, args...).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != 0 {
			t.Fatalf("%s survived finalization: %d", check.name, count)
		}
	}
}

func TestSameNameUserCannotInheritFinalizedAuthorization(t *testing.T) {
	repo, _ := newManagedNodesTestRepository(t)
	ctx, server, _, offer := seedManagedNodesTest(t, repo)
	now := time.Now().UTC()
	createManagedGrantForTest(t, repo, ctx, server.ID, now)
	if _, err := repo.ActivateUserNodeSelection(ctx, "alice", offer.ID, "alice", now); err != nil {
		t.Fatalf("ActivateUserNodeSelection: %v", err)
	}
	sources, err := repo.PrepareUserDeletion(ctx, "alice", "admin")
	if err != nil {
		t.Fatalf("PrepareUserDeletion: %v", err)
	}
	if err := repo.CreateUser(ctx, "alice", "replacement@example.test", "Replacement", "hash", RoleUser, ""); !errors.Is(err, ErrUserExists) {
		t.Fatalf("same-name creation bypassed tombstone: %v", err)
	}
	for _, source := range sources {
		if _, err := repo.MarkUserInboundAccessSourceApplied(ctx, source.ID, source.Generation,
			ManagedObservedInactive, now.Add(time.Second)); err != nil {
			t.Fatalf("apply source %d: %v", source.ID, err)
		}
	}
	if err := repo.FinalizeUserDeletion(ctx, "alice", "admin"); err != nil {
		t.Fatalf("FinalizeUserDeletion: %v", err)
	}
	if err := repo.CreateUser(ctx, "alice", "replacement@example.test", "Replacement", "new-hash", RoleUser, ""); err != nil {
		t.Fatalf("create replacement user: %v", err)
	}
	grants, err := repo.ListUserServerGrants(ctx, "alice")
	if err != nil || len(grants) != 0 {
		t.Fatalf("replacement inherited grants: grants=%#v err=%v", grants, err)
	}
	sources, err = repo.ListUserInboundAccessSources(ctx, "alice", 0)
	if err != nil || len(sources) != 0 {
		t.Fatalf("replacement inherited sources: sources=%#v err=%v", sources, err)
	}
}

func TestDeleteUserStorageWrapperNeverDropsPendingCredential(t *testing.T) {
	repo, _ := newManagedNodesTestRepository(t)
	ctx, server, _, _ := seedManagedNodesTest(t, repo)
	if err := repo.SaveUserInboundConfig(ctx, UserInboundConfig{
		Username: "alice", ServerID: server.ID, InboundTag: "pending-in", Protocol: "vless",
		CredentialJSON: `{"id":"pending-uuid","email":"alice__pending-in"}`,
	}); err != nil {
		t.Fatalf("SaveUserInboundConfig: %v", err)
	}

	if err := repo.DeleteUser(ctx, "alice"); !errors.Is(err, ErrUserDeletionPending) {
		t.Fatalf("DeleteUser error=%v want=%v", err, ErrUserDeletionPending)
	}
	if _, err := repo.GetUser(ctx, "alice"); err != nil {
		t.Fatalf("pending user was removed: %v", err)
	}
	configs, err := repo.GetUserInboundConfigs(ctx, "alice")
	if err != nil || len(configs) != 1 {
		t.Fatalf("pending credential was removed: configs=%#v err=%v", configs, err)
	}
	sources, err := repo.ListUserInboundAccessSources(ctx, "alice", 0)
	if err != nil || len(sources) != 1 {
		t.Fatalf("credential revocation source missing: sources=%#v err=%v", sources, err)
	}
	if _, err := repo.MarkUserInboundAccessSourceApplied(ctx, sources[0].ID, sources[0].Generation,
		ManagedObservedInactive, time.Now().UTC()); err != nil {
		t.Fatalf("apply remote revoke: %v", err)
	}
	if err := repo.DeleteUser(ctx, "alice"); err != nil {
		t.Fatalf("DeleteUser after applied revoke: %v", err)
	}
	if _, err := repo.GetUser(ctx, "alice"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("finalized user survived: %v", err)
	}
}
