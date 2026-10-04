package integrations

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGitHubJWTUsesBoundedRSAClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	jwt, err := githubJWT(key, "Iv1.fixture", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal("malformed JWT")
	}
	var claims struct {
		Iss      string
		Iat, Exp int64
	}
	plain, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if json.Unmarshal(plain, &claims) != nil || claims.Iss != "Iv1.fixture" || claims.Iat != now.Add(-time.Minute).Unix() || claims.Exp != now.Add(9*time.Minute).Unix() {
		t.Fatal("incorrect JWT lifetime")
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], signature); err != nil {
		t.Fatal(err)
	}
}
