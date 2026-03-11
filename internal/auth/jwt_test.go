package auth

import (
	"testing"
	"time"
)

func TestNewService_Expiration(t *testing.T) {
	svc := NewService("test-secret")
	expected := 180 * 24 * time.Hour
	if svc.expiration != expected {
		t.Fatalf("expected expiration %v, got %v", expected, svc.expiration)
	}
}

func TestGenerateToken_ValidClaims(t *testing.T) {
	svc := NewService("test-secret")
	token, err := svc.GenerateToken("device-1", "My iPhone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}
	if claims.DeviceID != "device-1" {
		t.Errorf("expected DeviceID=device-1, got %q", claims.DeviceID)
	}
	if claims.DeviceName != "My iPhone" {
		t.Errorf("expected DeviceName='My iPhone', got %q", claims.DeviceName)
	}
}

func TestValidateToken_WrongSecret(t *testing.T) {
	svc1 := NewService("secret-a")
	svc2 := NewService("secret-b")

	token, _ := svc1.GenerateToken("device-1", "iPhone")
	if _, err := svc2.ValidateToken(token); err == nil {
		t.Fatal("expected error for token signed with different secret")
	}
}

func TestValidateToken_Expired(t *testing.T) {
	svc := &Service{
		secret:     []byte("test-secret"),
		issuer:     "dockertab-agent",
		expiration: -1 * time.Hour, // already expired
	}
	token, err := svc.GenerateToken("device-1", "iPhone")
	if err != nil {
		t.Fatalf("unexpected error generating token: %v", err)
	}
	if _, err := svc.ValidateToken(token); err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestValidateToken_Tampered(t *testing.T) {
	svc := NewService("test-secret")
	token, _ := svc.GenerateToken("device-1", "iPhone")

	tampered := token[:len(token)-1] + "X"
	if _, err := svc.ValidateToken(tampered); err == nil {
		t.Fatal("expected error for tampered token")
	}
}

func TestValidateToken_Empty(t *testing.T) {
	svc := NewService("test-secret")
	if _, err := svc.ValidateToken(""); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestValidateToken_IssuerClaim(t *testing.T) {
	svc := NewService("test-secret")
	token, _ := svc.GenerateToken("device-1", "iPhone")

	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Issuer != "dockertab-agent" {
		t.Errorf("expected issuer=dockertab-agent, got %q", claims.Issuer)
	}
}

func TestToken_ExpiresAtIs180Days(t *testing.T) {
	svc := NewService("test-secret")
	before := time.Now()
	token, _ := svc.GenerateToken("device-1", "iPhone")
	after := time.Now()

	claims, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expiresAt := claims.ExpiresAt.Time
	lower := before.Add(180 * 24 * time.Hour).Add(-time.Second)
	upper := after.Add(180 * 24 * time.Hour).Add(time.Second)
	if expiresAt.Before(lower) || expiresAt.After(upper) {
		t.Errorf("expected expiry around 180 days from now, got %v", expiresAt)
	}
}
