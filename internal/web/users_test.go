package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUserStoreBootstrapAdminAndAuthenticate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store := NewUserStore(path)
	if err := store.BootstrapAdmin("Admin", "test-password-strong"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("users file mode = %o, want 0600", got)
	}

	user, err := store.Authenticate("admin", "test-password-strong")
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

func TestBootstrapPasswordCanBeRemovedAfterInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store := NewUserStore(path)
	if err := store.BootstrapAdmin("admin", ""); err == nil {
		t.Fatal("empty password initialized the first admin")
	}
	if err := store.BootstrapAdmin("admin", "test-password-strong"); err != nil {
		t.Fatal(err)
	}
	if err := NewUserStore(path).BootstrapAdmin("admin", ""); err != nil {
		t.Fatalf("existing users still required bootstrap secret: %v", err)
	}
}

func TestUserStoreCreateDefaultsToUserRole(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	user, err := store.Create("Researcher", "test-password-strong", "")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "researcher" || user.Role != RoleUser {
		t.Fatalf("created user = %+v, want normalized user role", user)
	}
}

func TestUserStoreChangePassword(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if _, err := store.Create("alice", "old-password-strong", RoleUser); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangePassword("alice", "wrong", "new-password-strong"); err == nil {
		t.Fatalf("wrong current password changed password")
	}
	if _, err := store.ChangePassword("alice", "old-password-strong", "new-password-strong"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate("alice", "old-password-strong"); err == nil {
		t.Fatalf("old password still authenticated")
	}
	if _, err := store.Authenticate("alice", "new-password-strong"); err != nil {
		t.Fatalf("new password did not authenticate: %v", err)
	}
}

func TestUserStoreDelete(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if err := store.BootstrapAdmin("admin", "test-password-strong"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("alice", "test-password-strong", RoleUser); err != nil {
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
	if _, err := store.Create("alice", "replacement-secret", RoleUser); err == nil {
		t.Fatal("recreated deleted username and could inherit its resources")
	}
}

func TestPBKDF2SHA256KnownVector(t *testing.T) {
	got := hex.EncodeToString(pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32))
	const want = "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if got != want {
		t.Fatalf("PBKDF2 vector = %s, want %s", got, want)
	}
}

func TestUserStoreMigratesLegacySHA256Hash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	salt := []byte("0123456789abcdef")
	digest := sha256.Sum256(append(append([]byte(nil), salt...), []byte("legacy-secret")...))
	legacy := fmt.Sprintf("%s$%s$%s", legacyPasswordHashScheme, hex.EncodeToString(salt), hex.EncodeToString(digest[:]))
	now := time.Now().UTC()
	data, err := json.Marshal([]UserRecord{{
		Username:     "alice",
		Role:         RoleUser,
		PasswordHash: legacy,
		CreatedAt:    now,
		UpdatedAt:    now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewUserStore(path)
	if _, err := store.Authenticate("alice", "legacy-secret"); err != nil {
		t.Fatalf("legacy login failed: %v", err)
	}
	record, found, err := store.Get("alice")
	if err != nil || !found {
		t.Fatalf("migrated user found=%v err=%v", found, err)
	}
	if !strings.HasPrefix(record.PasswordHash, passwordHashScheme+"$") {
		t.Fatalf("password hash was not migrated: %q", record.PasswordHash)
	}
}

func TestFailedLegacyPasswordRunsPBKDFPadding(t *testing.T) {
	salt := []byte("0123456789abcdef")
	digest := sha256.Sum256(append(append([]byte(nil), salt...), []byte("legacy-secret")...))
	legacy := fmt.Sprintf("%s$%s$%s", legacyPasswordHashScheme, hex.EncodeToString(salt), hex.EncodeToString(digest[:]))

	started := time.Now()
	if verifyPasswordHash(legacy, "wrong-password") {
		t.Fatal("wrong legacy password verified")
	}
	if elapsed := time.Since(started); elapsed < time.Millisecond {
		t.Fatalf("failed legacy verification returned in %v without PBKDF padding", elapsed)
	}
}

func TestUserStoreAllowsShortLegacyPasswordUntilChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	salt := []byte("0123456789abcdef")
	digest := sha256.Sum256(append(append([]byte(nil), salt...), []byte("change-me")...))
	legacy := fmt.Sprintf("%s$%s$%s", legacyPasswordHashScheme, hex.EncodeToString(salt), hex.EncodeToString(digest[:]))
	now := time.Now().UTC()
	data, err := json.Marshal([]UserRecord{{
		Username: "admin", Role: RoleAdmin, PasswordHash: legacy, CreatedAt: now, UpdatedAt: now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	store := NewUserStore(path)
	if _, err := store.Authenticate("admin", "change-me"); err != nil {
		t.Fatalf("legacy short password could not log in for migration: %v", err)
	}
	record, found, err := store.Get("admin")
	if err != nil || !found {
		t.Fatalf("migrated legacy user found=%v err=%v", found, err)
	}
	if !strings.HasPrefix(record.PasswordHash, passwordHashScheme+"$") {
		t.Fatalf("short legacy password hash was not migrated: %q", record.PasswordHash)
	}
	if _, err := store.ChangePassword("admin", "change-me", "replacement-password"); err != nil {
		t.Fatalf("legacy user could not set a strong password: %v", err)
	}
}

func TestUserStoreRejectsPasswordWorkWhenWorkersAreBusy(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if _, err := store.Create("alice", "test-password-strong", RoleUser); err != nil {
		t.Fatal(err)
	}
	for range maxConcurrentPasswordWork {
		store.passwordWork <- struct{}{}
	}
	defer func() {
		for range maxConcurrentPasswordWork {
			<-store.passwordWork
		}
	}()
	started := time.Now()
	if _, err := store.Authenticate("alice", "test-password-strong"); !errors.Is(err, errPasswordWorkBusy) {
		t.Fatalf("Authenticate error = %v, want %v", err, errPasswordWorkBusy)
	}
	if _, err := store.Create("bob", "test-password-strong", RoleUser); !errors.Is(err, errPasswordWorkBusy) {
		t.Fatalf("Create error = %v, want %v", err, errPasswordWorkBusy)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("busy authentication blocked for %v", elapsed)
	}
}

func TestPasswordQueueAdmitsTwentyConcurrentLogins(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	const users = 20
	start := make(chan struct{})
	releaseAll := make(chan struct{})
	done := make(chan error, users)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range users {
		go func() {
			<-start
			release, err := store.acquirePasswordWork(ctx, true)
			if err != nil {
				done <- err
				return
			}
			<-releaseAll
			release()
			done <- nil
		}()
	}
	close(start)
	deadline := time.Now().Add(2 * time.Second)
	for len(store.passwordQueue) != users && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(store.passwordQueue); got != users {
		close(releaseAll)
		t.Fatalf("queued password requests = %d, want %d", got, users)
	}
	if got := len(store.passwordWork); got != maxConcurrentPasswordWork {
		close(releaseAll)
		t.Fatalf("active password workers = %d, want %d", got, maxConcurrentPasswordWork)
	}
	close(releaseAll)
	for range users {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(store.passwordQueue) != 0 || len(store.passwordWork) != 0 {
		t.Fatalf("password admission did not drain: queued=%d active=%d", len(store.passwordQueue), len(store.passwordWork))
	}
}

func TestPasswordQueueReleasesCanceledWaiter(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	for range maxConcurrentPasswordWork {
		store.passwordWork <- struct{}{}
	}
	defer func() {
		for range maxConcurrentPasswordWork {
			<-store.passwordWork
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := store.acquirePasswordWork(ctx, true)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(store.passwordQueue) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(store.passwordQueue) != 1 {
		t.Fatal("password request did not enter the waiting queue")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queue error = %v", err)
	}
	if len(store.passwordQueue) != 0 {
		t.Fatalf("canceled password request leaked a queue slot")
	}
}

func TestPasswordQueueRejectsAlreadyCanceledContext(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.acquirePasswordWork(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled queue error = %v", err)
	}
	if len(store.passwordQueue) != 0 || len(store.passwordWork) != 0 {
		t.Fatalf("canceled password request was admitted: queued=%d active=%d", len(store.passwordQueue), len(store.passwordWork))
	}
}

func TestUserStoreGetDoesNotWaitForPasswordHashing(t *testing.T) {
	store := NewUserStore(filepath.Join(t.TempDir(), "users.json"))
	if _, err := store.Create("alice", "test-password-strong", RoleUser); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxConcurrentPasswordWork-1; i++ {
		store.passwordWork <- struct{}{}
	}
	authDone := make(chan error, 1)
	go func() {
		_, err := store.Authenticate("alice", "wrong-password")
		authDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(store.passwordWork) != maxConcurrentPasswordWork && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(store.passwordWork) != maxConcurrentPasswordWork {
		t.Fatal("authentication did not begin password hashing")
	}

	getDone := make(chan error, 1)
	go func() {
		_, _, err := store.Get("alice")
		getDone <- err
	}()
	select {
	case err := <-getDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Get waited for password hashing")
	}
	for i := 0; i < maxConcurrentPasswordWork-1; i++ {
		<-store.passwordWork
	}
	if err := <-authDone; err == nil {
		t.Fatal("wrong password authenticated")
	}
}
