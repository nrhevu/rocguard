package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"rocguardd/internal/config"
	"rocguardd/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return New(config.Config{
		StatePath:   filepath.Join(dir, "state.json"),
		RootKeyPath: filepath.Join(dir, "root.key"),
		AuditLog:    filepath.Join(dir, "audit.log"),
	})
}

func TestRootKeyAndTokenLifecycle(t *testing.T) {
	st := testStore(t)
	key, err := st.ReadOrCreateRootKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	secret, token, err := st.RegisterToken(key, "alice", "1h", now)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || token.Name != "alice" {
		t.Fatalf("unexpected token: secret=%q token=%+v", secret, token)
	}
	if _, _, err := st.ValidateToken(secret, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("token should be valid: %v", err)
	}
	if _, _, err := st.ValidateToken(secret, now.Add(2*time.Hour)); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("got %v, want ErrTokenExpired", err)
	}
	if err := st.Revoke(secret); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ValidateToken(secret, now.Add(30*time.Minute)); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("got %v, want ErrTokenRevoked", err)
	}
}

func TestRegisterRejectsTTLAboveMax(t *testing.T) {
	st := testStore(t)
	key, err := st.ReadOrCreateRootKey()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.RegisterToken(key, "alice", "25h", time.Now())
	if err == nil {
		t.Fatal("expected ttl error")
	}
}

func TestHardRegisterTTLMax(t *testing.T) {
	st := testStore(t)
	key, err := st.ReadOrCreateRootKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	if _, token, reservation, err := st.RegisterHardReservation(key, "alice", 0, "8h", now); err != nil {
		t.Fatalf("8h reserved reservation should be accepted: %v", err)
	} else if token.Mode != model.TokenModeReserved || reservation.GPU != 0 {
		t.Fatalf("unexpected reserved token/reservation: token=%+v reservation=%+v", token, reservation)
	}
	if _, _, _, err := st.RegisterHardReservation(key, "bob", 1, "8h1s", now); err == nil {
		t.Fatal("expected reserved ttl above 8h to fail")
	}
}

func TestSoftRegisterHasNoExpiry(t *testing.T) {
	st := testStore(t)
	key, err := st.ReadOrCreateRootKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	secret, token, err := st.RegisterSoftToken(key, "alice", now)
	if err != nil {
		t.Fatal(err)
	}
	if token.Mode != model.TokenModeClaimed || !token.ExpiresAt.IsZero() {
		t.Fatalf("unexpected claimed token: %+v", token)
	}
	if _, _, err := st.ValidateToken(secret, now.Add(365*24*time.Hour)); err != nil {
		t.Fatalf("claimed token should not expire: %v", err)
	}
}

func TestRevokeTokenRevokesRelatedState(t *testing.T) {
	st := testStore(t)
	key, err := st.ReadOrCreateRootKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	secret, token, reservation, err := st.RegisterHardReservation(key, "alice", 0, "1h", now)
	if err != nil {
		t.Fatal(err)
	}
	authorization := model.Authorization{
		ID:        NewAuthorizationID(),
		Mode:      model.ModeUser,
		TokenHash: token.Hash,
		TokenMode: token.Mode,
		Holder:    token.Name,
		UID:       1000,
		CreatedAt: now,
		ExpiresAt: token.ExpiresAt,
		Active:    true,
	}
	if err := st.AddAuthorization(authorization); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSoftClaim(model.SoftClaim{GPU: 0, TokenHash: token.Hash, AuthorizationID: authorization.ID, Holder: token.Name}, now); err != nil {
		t.Fatal(err)
	}
	bypass := model.BypassRule{
		ID:        NewBypassID(),
		Type:      model.BypassPID,
		PID:       123,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := st.AddBypass(bypass); err != nil {
		t.Fatal(err)
	}
	if err := st.Revoke(secret); err != nil {
		t.Fatal(err)
	}
	state, err := st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Tokens[0].Revoked {
		t.Fatal("token should be revoked")
	}
	if state.Reservations[0].ID != reservation.ID || state.Reservations[0].Active || !state.Reservations[0].Revoked {
		t.Fatalf("reservation should be inactive/revoked: %+v", state.Reservations[0])
	}
	if state.Authorizations[0].Active || !state.Authorizations[0].Revoked {
		t.Fatalf("authorization should be inactive/revoked: %+v", state.Authorizations[0])
	}
	if len(state.SoftClaims) != 0 {
		t.Fatalf("claims should be removed: %+v", state.SoftClaims)
	}
	if err := st.Revoke(bypass.ID); err != nil {
		t.Fatal(err)
	}
	state, err = st.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Bypasses[0].Revoked {
		t.Fatalf("bypass should be revoked: %+v", state.Bypasses[0])
	}
}
