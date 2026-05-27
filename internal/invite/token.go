package invite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type Payload struct {
	Version      int       `json:"ver"`
	TokenID      string    `json:"jti"`
	Issuer       string    `json:"iss"`
	Grantee      string    `json:"grn"`
	TargetNode   string    `json:"tgt_node"`
	ServiceName  string    `json:"svc_name"`
	ArtifactHash string    `json:"art_hash"`
	ExpiresAt    time.Time `json:"exp"`
	SingleUse    bool      `json:"single"`
}

type Envelope struct {
	Payload   Payload `json:"payload"`
	Signature string  `json:"signature"`
}

func NewPayload(issuer, grantee, targetNode, serviceName, artifactHash string, expiresAt time.Time, singleUse bool) (Payload, error) {
	tokenID, err := randomTokenID()
	if err != nil {
		return Payload{}, err
	}
	return Payload{
		Version:      1,
		TokenID:      tokenID,
		Issuer:       issuer,
		Grantee:      grantee,
		TargetNode:   targetNode,
		ServiceName:  serviceName,
		ArtifactHash: artifactHash,
		ExpiresAt:    expiresAt.UTC(),
		SingleUse:    singleUse,
	}, nil
}

func Sign(payload Payload, privateKey ed25519.PrivateKey) (string, error) {
	body, err := canonicalPayload(payload)
	if err != nil {
		return "", err
	}
	env := Envelope{
		Payload:   payload,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, body)),
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func ParseAndVerify(token string, publicKey ed25519.PublicKey) (*Payload, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("decode token: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}
	body, err := canonicalPayload(env.Payload)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(publicKey, body, sig) {
		return nil, fmt.Errorf("invalid signature")
	}
	return &env.Payload, nil
}

func canonicalPayload(payload Payload) ([]byte, error) {
	return json.Marshal(payload)
}

func randomTokenID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
