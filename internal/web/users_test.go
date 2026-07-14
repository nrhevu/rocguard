package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserStoreBootstrapAdminAndAuthenticate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store := NewUserStore(path)
	if err := store.BootstrapAdmin("Admin", "secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("users file mode = %o, want 0600", got)
	}

	user, err := store.Authenticate("admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "admin" || user.Role != RoleAdmin {
		t.Fatalf("bootstrap user = %+v, want admin role", user)
	}
	if _, err := store.Authenticate("admin", "wrong"); err == nil {
		t.Fatalf("wrong password authenticated")
	}
}

func TestUserStoreCreateDefaultsToUserRole(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	user, err := store.Create("Researcher", "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "researcher" || user.Role != RoleUser {
		t.Fatalf("created user = %+v, want normalized user role", user)
	}
}

func TestUserStoreChangePassword(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if _, err := store.Create("alice", "old-secret", RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangePassword("alice", "wrong", "new-secret"); err == nil {
		t.Fatalf("wrong current password changed password")
	}
	if _, err := store.ChangePassword("alice", "old-secret", "new-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate("alice", "old-secret"); err == nil {
		t.Fatalf("old password still authenticated")
	}
	if _, err := store.Authenticate("alice", "new-secret"); err != nil {
		t.Fatalf("new password did not authenticate: %v", err)
	}
}

func TestUserStoreDelete(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if err := store.BootstrapAdmin("admin", "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("alice", "secret", RoleUser); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get("alice"); err != nil || found {
		t.Fatalf("deleted user found=%v, err=%v", found, err)
	}
	if err := store.Delete("admin"); err == nil {
		t.Fatalf("deleted the last admin")
	}
}
