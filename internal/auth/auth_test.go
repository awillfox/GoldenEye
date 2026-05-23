package auth_test

import (
	"testing"

	"golden_eye/internal/auth"
)

const testKey = "test-master-key-must-be-32-chars!!"

func TestHashAndCheck(t *testing.T) {
	svc := auth.New(testKey)

	hash, err := svc.HashPassword("supersecret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if !svc.CheckPassword(hash, "supersecret") {
		t.Error("CheckPassword should return true for correct password")
	}
	if svc.CheckPassword(hash, "wrongpassword") {
		t.Error("CheckPassword should return false for wrong password")
	}
}

func TestIssueAndVerifyToken(t *testing.T) {
	svc := auth.New(testKey)

	token, err := svc.IssueToken(42, "alice", auth.RoleAdmin)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := svc.VerifyToken(token)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID: got %d, want 42", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username: got %q, want alice", claims.Username)
	}
	if claims.Role != auth.RoleAdmin {
		t.Errorf("Role: got %q, want admin", claims.Role)
	}
}

func TestVerifyToken_InvalidKey(t *testing.T) {
	svc := auth.New(testKey)
	otherSvc := auth.New("different-key-also-32-characters!!")

	token, _ := svc.IssueToken(1, "bob", auth.RoleViewer)
	_, err := otherSvc.VerifyToken(token)
	if err == nil {
		t.Error("expected error when verifying with wrong key")
	}
}

func TestVerifyToken_Tampered(t *testing.T) {
	svc := auth.New(testKey)
	token, _ := svc.IssueToken(1, "bob", auth.RoleViewer)
	_, err := svc.VerifyToken(token + "tampered")
	if err == nil {
		t.Error("expected error for tampered token")
	}
}
