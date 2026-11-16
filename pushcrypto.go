package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Web push, with nothing but Go's own crypto: a message to a phone goes
// through its browser's push service (Google, Apple, Mozilla, Microsoft),
// encrypted so only that phone can read it (RFC 8291) and signed with the
// site's own key so the push service knows who sent it (VAPID, RFC 8292).

var b64 = base64.RawURLEncoding

// newVAPIDKey makes the site's push signing key, kept in the data file.
func newVAPIDKey() (string, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(der), nil
}

func parseVAPIDKey(s string) (*ecdsa.PrivateKey, error) {
	der, err := b64.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return x509.ParseECPrivateKey(der)
}

// vapidPublic is the key browsers subscribe with (applicationServerKey): the
// public point, uncompressed, in URL-safe base64.
func vapidPublic(k *ecdsa.PrivateKey) (string, error) {
	pub, err := k.PublicKey.ECDH()
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(pub.Bytes()), nil
}

// vapidAuth is the Authorization header for one push service: a short-lived
// token signed with the site's key, naming the service and the site.
func vapidAuth(k *ecdsa.PrivateKey, endpoint, subject string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signing := head + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64) // ES256: r then s, 32 bytes each
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	pub, err := vapidPublic(k)
	if err != nil {
		return "", err
	}
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + pub, nil
}

// pushRecordSize is the record size in the message header. A message is one
// record, so it only has to be bigger than the message.
const pushRecordSize = 4096

// encryptPush seals a message for one browser subscription (its p256dh key
// and auth secret), as aes128gcm content (RFC 8188, RFC 8291).
func encryptPush(plain []byte, p256dh, auth string) ([]byte, error) {
	uaPub, err := decodeKey(p256dh)
	if err != nil {
		return nil, err
	}
	secret, err := decodeKey(auth)
	if err != nil {
		return nil, err
	}
	if len(secret) != 16 {
		return nil, errors.New("push auth secret isn't 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return sealPush(plain, ua, secret, as, salt)
}

// sealPush does encryptPush's work with the server's one-time key and salt
// given, so tests can check it against the RFC's worked example.
func sealPush(plain []byte, ua *ecdh.PublicKey, authSecret []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, err
	}
	keyInfo := "WebPush: info\x00" + string(ua.Bytes()) + string(asPub)
	ikm, err := hkdf.Expand(sha256.New, prkKey, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(plain)+1+gcm.Overhead() > pushRecordSize {
		return nil, errors.New("push message too long")
	}
	record := append(append([]byte{}, plain...), 0x02) // 2: the last (only) record, no padding
	head := make([]byte, 0, 16+4+1+len(asPub))
	head = append(head, salt...)
	head = binary.BigEndian.AppendUint32(head, pushRecordSize)
	head = append(head, byte(len(asPub)))
	head = append(head, asPub...)
	return gcm.Seal(head, nonce, record, nil), nil
}

// decodeKey reads a key from a subscription, which browsers send in URL-safe
// base64, with or without padding.
func decodeKey(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}
