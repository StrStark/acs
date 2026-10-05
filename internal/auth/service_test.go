package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"acs/internal/db"
	"acs/internal/secret"
)

func newTestService(t *testing.T, setupToken string) *Service {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewService(database, box, time.Hour, setupToken)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSetupThenLogin(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, "")

	if needs, _ := s.NeedsSetup(ctx); !needs {
		t.Fatal("fresh database should need setup")
	}
	if _, err := s.Setup(ctx, "admin", "short", ""); err == nil {
		t.Fatal("expected short password to be rejected")
	}
	if _, err := s.Setup(ctx, "admin", "correct horse battery", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Setup(ctx, "other", "correct horse battery", ""); !errors.Is(err, ErrSetupComplete) {
		t.Fatalf("second setup: got %v, want ErrSetupComplete", err)
	}
	if needs, _ := s.NeedsSetup(ctx); needs {
		t.Fatal("setup should be complete")
	}

	if _, _, err := s.Login(ctx, "admin", "wrong password!", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: got %v", err)
	}
	if _, _, err := s.Login(ctx, "nobody", "correct horse battery", "", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: got %v", err)
	}

	token, _, err := s.Login(ctx, "ADMIN", "correct horse battery", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Authenticate(ctx, token)
	if err != nil || u.Username != "admin" {
		t.Fatalf("authenticate: %v %+v", err, u)
	}

	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after logout: got %v", err)
	}
}

func TestSetupToken(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, "s3cret")
	if _, err := s.Setup(ctx, "admin", "correct horse battery", "nope"); !errors.Is(err, ErrBadSetupToken) {
		t.Fatalf("got %v, want ErrBadSetupToken", err)
	}
	if _, err := s.Setup(ctx, "admin", "correct horse battery", "s3cret"); err != nil {
		t.Fatal(err)
	}
}

func TestAccessKeysAndRoles(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, "")
	admin, _ := s.Setup(ctx, "admin", "correct horse battery", "")
	viewer, err := s.CreateUser(ctx, "viv", "viewer password", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "VIV", "viewer password", RoleViewer); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate user: %v", err)
	}

	k, sec, err := s.CreateAccessKey(ctx, admin.ID, CreateKeyInput{Name: "ci", Permission: KeyReadWrite, Buckets: []string{"builds"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateKey(ctx, k.ID, sec)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Can(ActWrite, "builds") || p.Can(ActWrite, "other") || p.Can(ActManageBuckets, "builds") || p.Can(ActAdmin, "") {
		t.Fatal("key scope not enforced")
	}
	if _, err := s.AuthenticateKey(ctx, k.ID, "wrong"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("wrong secret: %v", err)
	}

	vk, vsecret, _ := s.CreateAccessKey(ctx, viewer.ID, CreateKeyInput{Name: "x", Permission: KeyFull})
	vp, _ := s.AuthenticateKey(ctx, vk.ID, vsecret)
	if !vp.Can(ActRead, "any") || vp.Can(ActWrite, "any") {
		t.Fatal("viewer key must stay read-only")
	}

	// Deleting the user removes their keys.
	if err := s.DeleteUser(ctx, viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateKey(ctx, vk.ID, vsecret); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("deleted user's key: %v", err)
	}
	if err := s.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin: %v", err)
	}
	if _, err := s.UpdateUser(ctx, admin.ID, RoleEditor, ""); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote last admin: %v", err)
	}
}
