package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMmwImportBlockingCountsAllowsOnlyCurrentAdminScaffolding(t *testing.T) {
	repo, err := NewTrafficRepository(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.CreateUser(ctx, "owner", "", "Owner", "hash", RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO user_settings(username) VALUES (?)`, "owner"); err != nil {
		t.Fatal(err)
	}
	counts, err := repo.MmwImportBlockingCounts(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 0 {
		t.Fatalf("admin scaffolding was treated as business data: %#v", counts)
	}
	if err := repo.CreateUser(ctx, "existing", "", "Existing", "hash", RoleUser, ""); err != nil {
		t.Fatal(err)
	}
	counts, err = repo.MmwImportBlockingCounts(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if counts["users"] != 1 {
		t.Fatalf("users blocking count=%d, want 1; all=%#v", counts["users"], counts)
	}
}

func TestImportFromMmwKeepsAuthenticatedAdminAndAssignsOwnership(t *testing.T) {
	root := t.TempDir()
	repo, err := NewTrafficRepository(filepath.Join(root, "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.CreateUser(ctx, "current-admin", "", "Current", "hash", RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE users SET created_at = '2025-01-01 00:00:00' WHERE username = 'current-admin'`); err != nil {
		t.Fatal(err)
	}

	sourcePath := filepath.Join(root, "source.db")
	source, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE users (username TEXT PRIMARY KEY, password_hash TEXT NOT NULL, role TEXT NOT NULL, created_at TIMESTAMP NOT NULL)`,
		`INSERT INTO users(username,password_hash,role,created_at) VALUES ('source-admin','hash','admin','2000-01-01 00:00:00')`,
		`CREATE TABLE templates (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO templates(id,name) VALUES (17,'Imported Template')`,
	}
	for _, statement := range statements {
		if _, err := source.Exec(statement); err != nil {
			_ = source.Close()
			t.Fatal(err)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ImportFromMmw(ctx, sourcePath, "current-admin"); err != nil {
		t.Fatal(err)
	}
	current, err := repo.GetUser(ctx, "current-admin")
	if err != nil {
		t.Fatal(err)
	}
	if current.Role != RoleAdmin {
		t.Fatalf("current admin role=%q, want admin", current.Role)
	}
	imported, err := repo.GetUser(ctx, "source-admin")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Role != RoleUser {
		t.Fatalf("source admin role=%q, want user", imported.Role)
	}
	var owner string
	if err := repo.db.QueryRowContext(ctx, `SELECT created_by FROM templates WHERE id = 17`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "current-admin" {
		t.Fatalf("template owner=%q, want current-admin", owner)
	}
}
