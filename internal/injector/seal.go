// Package injector implements a tokenizer-style credential injector
// (superfly/tokenizer): secrets are sealed to the injector's public key and
// handed to sandboxes as opaque blobs; the injector proxies sandbox egress,
// unseals blobs it finds in requests, verifies the client and the upstream
// host, and swaps the blob for the real credential. The sandbox never holds
// a usable secret and the injector holds no secret store.
package injector

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/crypto/nacl/box"
)

const sealedPrefix = "brk1_"

type Secret struct {
	Token        string   `json:"token"`
	AllowedHosts []string `json:"allowedHosts,omitempty"`
	AuthDigest   string   `json:"authDigest,omitempty"`
}

func GenerateKeypair() (publicKey, privateKey string, err error) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(pub[:]), base64.StdEncoding.EncodeToString(priv[:]), nil
}

func decodeKey(encoded string) (*[32]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes of base64")
	}
	var key [32]byte
	copy(key[:], raw)
	return &key, nil
}

func AuthDigest(authToken string) string {
	sum := sha256.Sum256([]byte(authToken))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func Seal(publicKey string, secret Secret) (string, error) {
	pub, err := decodeKey(publicKey)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	sealed, err := box.SealAnonymous(nil, payload, pub, rand.Reader)
	if err != nil {
		return "", err
	}
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func Open(publicKey, privateKey, sealed string) (Secret, error) {
	blob, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return Secret{}, fmt.Errorf("not a sealed secret")
	}
	pub, err := decodeKey(publicKey)
	if err != nil {
		return Secret{}, err
	}
	priv, err := decodeKey(privateKey)
	if err != nil {
		return Secret{}, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		return Secret{}, fmt.Errorf("malformed sealed secret")
	}
	payload, ok := box.OpenAnonymous(nil, raw, pub, priv)
	if !ok {
		return Secret{}, fmt.Errorf("sealed secret cannot be opened with this key")
	}
	var secret Secret
	if err := json.Unmarshal(payload, &secret); err != nil {
		return Secret{}, err
	}
	return secret, nil
}

func (s Secret) HostAllowed(host string) bool {
	if len(s.AllowedHosts) == 0 {
		return false
	}
	host = strings.ToLower(strings.Split(host, ":")[0])
	for _, allowed := range s.AllowedHosts {
		allowed = strings.ToLower(allowed)
		if host == allowed {
			return true
		}
		if suffix, ok := strings.CutPrefix(allowed, "*."); ok && strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func (s Secret) AuthOK(authToken string) bool {
	if s.AuthDigest == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(AuthDigest(authToken)), []byte(s.AuthDigest)) == 1
}
