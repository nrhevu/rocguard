package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rocguard/internal/config"
	"rocguard/internal/model"
	"rocguard/internal/protocol"
)

type authzNodeClient struct {
	mu              sync.Mutex
	snapshot        model.NodeSnapshot
	keys            model.KeyStatus
	lastReservation protocol.RegisterArgs
	lastClaim       protocol.RegisterArgs
	revoked         []string
}

func (c *authzNodeClient) Health(context.Context, ServerRecord, string) error {
	return nil
}

func (c *authzNodeClient) Snapshot(context.Context, ServerRecord) (model.NodeSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot, nil
}

func (c *authzNodeClient) CreateReservation(_ context.Context, _ ServerRecord, args protocol.RegisterArgs) (model.RegisterResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastReservation = args
	return model.RegisterResult{Token: "rg_reserved"}, nil
}

func (c *authzNodeClient) CreateClaimKey(_ context.Context, _ ServerRecord, args protocol.RegisterArgs) (model.RegisterResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastClaim = args
	return model.RegisterResult{Token: "rg_claim"}, nil
}

func (c *authzNodeClient) ShowKeys(context.Context, ServerRecord, string) (model.KeyStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys, nil
}

func (c *authzNodeClient) Revoke(_ context.Context, _ ServerRecord, args protocol.RevokeArgs) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, args.ID)
	return map[string]string{"revoked": args.ID}, nil
}

func TestGatewayRoleAuthorization(t *testing.T) {
	server, client, serverID := newAuthzServer(t)
	handler := server.routes()
	userCookie := testSessionCookie(t, server, "alice", RoleUser)
	adminCookie := testSessionCookie(t, server, "admin", RoleAdmin)

	userAdd := requestJSON(handler, http.MethodPost, "/api/servers", `{"name":"n","endpoint":"http://node","root_key":"rk"}`, userCookie)
	if userAdd.Code != http.StatusForbidden {
		t.Fatalf("user add server status = %d, body=%s", userAdd.Code, userAdd.Body.String())
	}

	userCreate := requestJSON(handler, http.MethodPost, "/api/users", `{"username":"bob","password":"secret"}`, userCookie)
	if userCreate.Code != http.StatusForbidden {
		t.Fatalf("user create user status = %d, body=%s", userCreate.Code, userCreate.Body.String())
	}

	adminCreate := requestJSON(handler, http.MethodPost, "/api/users", `{"username":"bob","password":"secret"}`, adminCookie)
	if adminCreate.Code != http.StatusCreated {
		t.Fatalf("admin create user status = %d, body=%s", adminCreate.Code, adminCreate.Body.String())
	}

	userDelete := requestJSON(handler, http.MethodDelete, "/api/users", `{"username":"bob"}`, userCookie)
	if userDelete.Code != http.StatusForbidden {
		t.Fatalf("user delete status = %d, body=%s", userDelete.Code, userDelete.Body.String())
	}

	selfDelete := requestJSON(handler, http.MethodDelete, "/api/users", `{"username":"admin"}`, adminCookie)
	if selfDelete.Code != http.StatusBadRequest {
		t.Fatalf("admin self-delete status = %d, body=%s", selfDelete.Code, selfDelete.Body.String())
	}

	adminDelete := requestJSON(handler, http.MethodDelete, "/api/users", `{"username":"bob"}`, adminCookie)
	if adminDelete.Code != http.StatusOK {
		t.Fatalf("admin delete user status = %d, body=%s", adminDelete.Code, adminDelete.Body.String())
	}
	if _, found, err := server.Users.Get("bob"); err != nil || found {
		t.Fatalf("deleted user found=%v, err=%v", found, err)
	}

	reserve := requestJSON(handler, http.MethodPost, "/api/servers/"+serverID+"/reservations", `{"name":"mallory","purpose":"test","gpus":[0]}`, userCookie)
	if reserve.Code != http.StatusCreated {
		t.Fatalf("reserve status = %d, body=%s", reserve.Code, reserve.Body.String())
	}
	client.mu.Lock()
	gotOwner := client.lastReservation.Name
	client.mu.Unlock()
	if gotOwner != "alice" {
		t.Fatalf("reservation owner = %q, want alice", gotOwner)
	}
}

func TestGatewayFiltersAndAuthorizesOwnedKeys(t *testing.T) {
	server, client, serverID := newAuthzServer(t)
	handler := server.routes()
	userCookie := testSessionCookie(t, server, "alice", RoleUser)
	adminCookie := testSessionCookie(t, server, "admin", RoleAdmin)

	snapshot := requestJSON(handler, http.MethodGet, "/api/fleet/snapshot", "", userCookie)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, body=%s", snapshot.Code, snapshot.Body.String())
	}
	var fleet fleetSnapshot
	if err := json.Unmarshal(snapshot.Body.Bytes(), &fleet); err != nil {
		t.Fatal(err)
	}
	tokens := fleet.Servers[0].Snapshot.Tokens
	if len(tokens) != 1 || tokens[0].Name != "alice" {
		t.Fatalf("filtered tokens = %+v, want only alice token", tokens)
	}
	if got := len(fleet.Servers[0].Snapshot.Reservations); got != 2 {
		t.Fatalf("visible reservations = %d, want 2", got)
	}

	ownKey := requestJSON(handler, http.MethodPost, "/api/servers/"+serverID+"/show-key", `{"id":"tok_alice"}`, userCookie)
	if ownKey.Code != http.StatusOK || !bytes.Contains(ownKey.Body.Bytes(), []byte("rg_alice")) {
		t.Fatalf("own show-key = %d %s", ownKey.Code, ownKey.Body.String())
	}

	otherKey := requestJSON(handler, http.MethodPost, "/api/servers/"+serverID+"/show-key", `{"id":"tok_bob"}`, userCookie)
	if otherKey.Code != http.StatusForbidden {
		t.Fatalf("other show-key = %d, body=%s", otherKey.Code, otherKey.Body.String())
	}

	otherRevoke := requestJSON(handler, http.MethodPost, "/api/servers/"+serverID+"/revoke", `{"id":"tok_bob"}`, userCookie)
	if otherRevoke.Code != http.StatusForbidden {
		t.Fatalf("other revoke = %d, body=%s", otherRevoke.Code, otherRevoke.Body.String())
	}

	ownRevoke := requestJSON(handler, http.MethodPost, "/api/servers/"+serverID+"/revoke", `{"id":"tok_alice"}`, userCookie)
	if ownRevoke.Code != http.StatusOK {
		t.Fatalf("own revoke = %d, body=%s", ownRevoke.Code, ownRevoke.Body.String())
	}

	adminRevoke := requestJSON(handler, http.MethodPost, "/api/servers/"+serverID+"/revoke", `{"id":"tok_bob"}`, adminCookie)
	if adminRevoke.Code != http.StatusOK {
		t.Fatalf("admin revoke = %d, body=%s", adminRevoke.Code, adminRevoke.Body.String())
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.revoked) != 2 || client.revoked[0] != "tok_alice" || client.revoked[1] != "tok_bob" {
		t.Fatalf("revoked ids = %+v, want tok_alice then tok_bob", client.revoked)
	}
}

func newAuthzServer(t *testing.T) (*Server, *authzNodeClient, string) {
	t.Helper()
	tmp := t.TempDir()
	server := New(config.Config{
		WebUser:     "admin",
		WebPassword: "secret",
		WebRegistry: filepath.Join(tmp, "servers.json"),
		WebUsers:    filepath.Join(tmp, "users.json"),
	})
	if err := server.Users.BootstrapAdmin("admin", "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Users.Create("alice", "secret", RoleUser); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0).UTC()
	client := &authzNodeClient{
		snapshot: model.NodeSnapshot{
			Now: now,
			Tokens: []model.TokenView{
				{ID: "tok_alice", Name: "alice", Mode: model.TokenModeReserved, CreatedAt: now},
				{ID: "tok_bob", Name: "bob", Mode: model.TokenModeReserved, CreatedAt: now},
			},
			Reservations: []model.ReservationView{
				{ID: "res_alice", GroupID: "tok_alice", GPU: 0, Holder: "alice", CreatedAt: now, StartsAt: now, ExpiresAt: now.Add(time.Hour), Active: true},
				{ID: "res_bob", GroupID: "tok_bob", GPU: 1, Holder: "bob", CreatedAt: now, StartsAt: now, ExpiresAt: now.Add(time.Hour), Active: true},
			},
		},
		keys: model.KeyStatus{
			Now: now,
			Tokens: []model.TokenView{
				{ID: "tok_alice", Key: "rg_alice", Name: "alice", Mode: model.TokenModeReserved, CreatedAt: now},
				{ID: "tok_bob", Key: "rg_bob", Name: "bob", Mode: model.TokenModeReserved, CreatedAt: now},
			},
			Reservations: []model.ReservationView{
				{ID: "res_alice", GroupID: "tok_alice", GPU: 0, Holder: "alice", CreatedAt: now, StartsAt: now, ExpiresAt: now.Add(time.Hour), Active: true},
				{ID: "res_bob", GroupID: "tok_bob", GPU: 1, Holder: "bob", CreatedAt: now, StartsAt: now, ExpiresAt: now.Add(time.Hour), Active: true},
			},
		},
	}
	server.Client = client
	stored, err := server.Registry.Upsert(ServerRecord{
		Name:     "node-a",
		Endpoint: "http://node-a:8443",
		RootKey:  "rk_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, client, stored.ID
}

func testSessionCookie(t *testing.T, server *Server, username, role string) *http.Cookie {
	t.Helper()
	expires := time.Now().Add(time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	return server.sessionCookie(req, server.signSession(username, role, expires), expires)
}

func requestJSON(handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}
