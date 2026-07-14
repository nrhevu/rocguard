package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"

	passwordHashScheme = "sha256"
)

type UserRecord struct {
	Username     string    `json:"username"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type PublicUser struct {
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserStore struct {
	mu   sync.Mutex
	path string
}

func NewUserStore(path string) *UserStore {
	return &UserStore{path: path}
}

func (s *UserStore) BootstrapAdmin(username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil
	}
	record, err := newUserRecord(username, password, RoleAdmin)
	if err != nil {
		return err
	}
	return s.saveLocked([]UserRecord{record})
}

func (s *UserStore) List() ([]PublicUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	out := make([]PublicUser, 0, len(users))
	for _, user := range users {
		out = append(out, publicUser(user))
	}
	return out, nil
}

func (s *UserStore) Get(username string) (UserRecord, bool, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return UserRecord{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return UserRecord{}, false, err
	}
	for _, user := range users {
		if user.Username == username {
			return user, true, nil
		}
	}
	return UserRecord{}, false, nil
}

func (s *UserStore) Authenticate(username, password string) (PublicUser, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return PublicUser{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return PublicUser{}, err
	}
	for _, user := range users {
		if user.Username != username {
			continue
		}
		if verifyPasswordHash(user.PasswordHash, password) {
			return publicUser(user), nil
		}
		return PublicUser{}, errors.New("invalid username or password")
	}
	return PublicUser{}, errors.New("invalid username or password")
}

func (s *UserStore) Create(username, password, role string) (PublicUser, error) {
	record, err := newUserRecord(username, password, role)
	if err != nil {
		return PublicUser{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return PublicUser{}, err
	}
	for _, user := range users {
		if user.Username == record.Username {
			return PublicUser{}, errors.New("user already exists")
		}
	}
	users = append(users, record)
	if err := s.saveLocked(users); err != nil {
		return PublicUser{}, err
	}
	return publicUser(record), nil
}

func (s *UserStore) Delete(username string) error {
	username, err := normalizeUsername(username)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, user := range users {
		if user.Username != username {
			continue
		}
		if user.Role == RoleAdmin {
			adminCount := 0
			for _, candidate := range users {
				if candidate.Role == RoleAdmin {
					adminCount++
				}
			}
			if adminCount == 1 {
				return errors.New("cannot delete the last admin")
			}
		}
		users = append(users[:i], users[i+1:]...)
		return s.saveLocked(users)
	}
	return errors.New("user not found")
}

func (s *UserStore) ChangePassword(username, currentPassword, newPassword string) (PublicUser, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return PublicUser{}, err
	}
	if strings.TrimSpace(newPassword) == "" {
		return PublicUser{}, errors.New("new password is required")
	}
	newHash, err := hashPassword(newPassword)
	if err != nil {
		return PublicUser{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	users, err := s.loadLocked()
	if err != nil {
		return PublicUser{}, err
	}
	for i := range users {
		if users[i].Username != username {
			continue
		}
		if !verifyPasswordHash(users[i].PasswordHash, currentPassword) {
			return PublicUser{}, errors.New("current password is invalid")
		}
		users[i].PasswordHash = newHash
		users[i].UpdatedAt = time.Now().UTC()
		if err := s.saveLocked(users); err != nil {
			return PublicUser{}, err
		}
		return publicUser(users[i]), nil
	}
	return PublicUser{}, errors.New("user not found")
}

func (s *UserStore) loadLocked() ([]UserRecord, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var users []UserRecord
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	return users, nil
}

func (s *UserStore) saveLocked(users []UserRecord) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	return os.Chmod(s.path, 0600)
}

func newUserRecord(username, password, role string) (UserRecord, error) {
	username, err := normalizeUsername(username)
	if err != nil {
		return UserRecord{}, err
	}
	role = normalizeRole(role)
	if strings.TrimSpace(password) == "" {
		return UserRecord{}, errors.New("password is required")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return UserRecord{}, err
	}
	now := time.Now().UTC()
	return UserRecord{
		Username:     username,
		Role:         role,
		PasswordHash: hash,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func normalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return "", errors.New("username is required")
	}
	for _, r := range username {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("username cannot contain whitespace or control characters")
		}
	}
	return username, nil
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleAdmin:
		return RoleAdmin
	default:
		return RoleUser
	}
}

func publicUser(user UserRecord) PublicUser {
	return PublicUser{
		Username:  user.Username,
		Role:      user.Role,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	}
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := passwordDigest(salt, password)
	return fmt.Sprintf("%s$%s$%s", passwordHashScheme, hex.EncodeToString(salt), hex.EncodeToString(sum)), nil
}

func verifyPasswordHash(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != passwordHashScheme {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got := passwordDigest(salt, password)
	return len(want) == len(got) && subtle.ConstantTimeCompare(want, got) == 1
}

func passwordDigest(salt []byte, password string) []byte {
	h := sha256.New()
	_, _ = h.Write(salt)
	_, _ = h.Write([]byte(password))
	return h.Sum(nil)
}
