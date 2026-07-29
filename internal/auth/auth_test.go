package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestGenerateAndVerify(t *testing.T) {
	a := New("test-secret")
	token, err := a.GenerateToken("testuser")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if token == "" {
		t.Fatal("empty token")
	}

	claims, err := a.VerifyToken(token)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Username != "testuser" {
		t.Errorf("username = %s, want testuser", claims.Username)
	}
}

func TestExpiredToken(t *testing.T) {
	a := New("test-secret")
	claims := Claims{
		Username:  "expireduser",
		CreatedAt: time.Now().Add(-15 * 24 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-1 * time.Hour).Unix(),
	}
	payload, _ := json.Marshal(claims)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	bPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := a.sign(header + "." + bPayload)
	token := header + "." + bPayload + "." + sig

	_, err := a.VerifyToken(token)
	if err == nil || err.Error() != "token expired" {
		t.Errorf("want token expired, got %v", err)
	}
}

func TestInvalidSignature(t *testing.T) {
	a := New("secret-a")
	b := New("secret-b")
	token, _ := a.GenerateToken("user")
	_, err := b.VerifyToken(token)
	if err == nil {
		t.Error("should reject wrong secret")
	}
}

func TestTokenExpiryDuration(t *testing.T) {
	if TokenExpiry != 14*24*time.Hour {
		t.Errorf("TokenExpiry = %v, want 336h", TokenExpiry)
	}
}
