package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rocguard/internal/config"
)

func TestSessionLoginProtectsAPIWithoutBasicPopup(t *testing.T) {
	server := New(config.Config{
		WebUser:     "admin",
		WebPassword: "secret",
		WebRegistry: filepath.Join(t.TempDir(), "servers.json"),
		WebUsers:    filepath.Join(t.TempDir(), "users.json"),
	})
	if err := server.Users.BootstrapAdmin("admin", "secret"); err != nil {
		t.Fatal(err)
	}
	handler := server.routes()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/servers", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}
	if header := unauthorized.Header().Get("WWW-Authenticate"); header != "" {
		t.Fatalf("WWW-Authenticate header = %q, want empty", header)
	}

	login := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"admin","password":"secret"}`)
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/login", body))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("login cookies = %+v, want %s", cookies, sessionCookieName)
	}

	session := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(cookies[0])
	handler.ServeHTTP(session, req)
	if session.Code != http.StatusOK ||
		!bytes.Contains(session.Body.Bytes(), []byte(`"authenticated":true`)) ||
		!bytes.Contains(session.Body.Bytes(), []byte(`"role":"admin"`)) {
		t.Fatalf("session response = %d %s", session.Code, session.Body.String())
	}

	authorized := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/servers", nil)
	req.AddCookie(cookies[0])
	handler.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body=%s", authorized.Code, authorized.Body.String())
	}
}

func TestSessionRegisterCreatesUserAndSession(t *testing.T) {
	server := New(config.Config{
		WebUser:     "admin",
		WebPassword: "secret",
		WebRegistry: filepath.Join(t.TempDir(), "servers.json"),
		WebUsers:    filepath.Join(t.TempDir(), "users.json"),
	})
	if err := server.Users.BootstrapAdmin("admin", "secret"); err != nil {
		t.Fatal(err)
	}
	handler := server.routes()

	registered := httptest.NewRecorder()
	body := strings.NewReader(`{"username":"alice","password":"alice-secret","role":"admin"}`)
	handler.ServeHTTP(registered, httptest.NewRequest(http.MethodPost, "/api/register", body))
	if registered.Code != http.StatusCreated ||
		!bytes.Contains(registered.Body.Bytes(), []byte(`"authenticated":true`)) ||
		!bytes.Contains(registered.Body.Bytes(), []byte(`"role":"user"`)) {
		t.Fatalf("register response = %d %s", registered.Code, registered.Body.String())
	}
	if cookies := registered.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("register cookies = %+v, want %s", cookies, sessionCookieName)
	}
	user, found, err := server.Users.Get("alice")
	if err != nil || !found {
		t.Fatalf("registered user = %+v, found=%v, err=%v", user, found, err)
	}
	if user.Role != RoleUser {
		t.Fatalf("registered role = %q, want %q", user.Role, RoleUser)
	}

	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(`{"username":"alice","password":"other"}`)))
	if duplicate.Code != http.StatusBadRequest {
		t.Fatalf("duplicate register status = %d, body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func TestSessionChangePassword(t *testing.T) {
	server := New(config.Config{
		WebUser:     "admin",
		WebPassword: "secret",
		WebRegistry: filepath.Join(t.TempDir(), "servers.json"),
		WebUsers:    filepath.Join(t.TempDir(), "users.json"),
	})
	if err := server.Users.BootstrapAdmin("admin", "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Users.Create("alice", "old-secret", RoleUser); err != nil {
		t.Fatal(err)
	}
	handler := server.routes()
	cookie := testSessionCookie(t, server, "alice", RoleUser)

	wrong := requestJSON(handler, http.MethodPost, "/api/password", `{"current_password":"wrong","new_password":"new-secret"}`, cookie)
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password status = %d, body=%s", wrong.Code, wrong.Body.String())
	}

	changed := requestJSON(handler, http.MethodPost, "/api/password", `{"current_password":"old-secret","new_password":"new-secret"}`, cookie)
	if changed.Code != http.StatusOK {
		t.Fatalf("change password status = %d, body=%s", changed.Code, changed.Body.String())
	}

	oldLogin := httptest.NewRecorder()
	handler.ServeHTTP(oldLogin, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"alice","password":"old-secret"}`)))
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old login status = %d, want unauthorized", oldLogin.Code)
	}
	newLogin := httptest.NewRecorder()
	handler.ServeHTTP(newLogin, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"alice","password":"new-secret"}`)))
	if newLogin.Code != http.StatusOK {
		t.Fatalf("new login status = %d, body=%s", newLogin.Code, newLogin.Body.String())
	}
}
