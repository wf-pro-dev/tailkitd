package invite

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	payload, err := NewPayload("alice@example.com", "bob@example.com", "tailkitd-a", "svc", "hash", time.Now().Add(time.Hour), true)
	if err != nil {
		t.Fatalf("NewPayload: %v", err)
	}
	token, err := Sign(payload, priv)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := ParseAndVerify(token, pub)
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	if got.TokenID != payload.TokenID {
		t.Fatalf("TokenID = %q, want %q", got.TokenID, payload.TokenID)
	}
}
