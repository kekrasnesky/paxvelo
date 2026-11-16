package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// The worked example in RFC 8291, Appendix A: the same keys and salt give
// the same bytes.
func TestPushEncryptRFCExample(t *testing.T) {
	mustKey := func(s string) []byte {
		b, err := decodeKey(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ua, err := ecdh.P256().NewPublicKey(mustKey("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"))
	if err != nil {
		t.Fatal(err)
	}
	as, err := ecdh.P256().NewPrivateKey(mustKey("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealPush([]byte("When I grow up, I want to be a watermelon"), ua, mustKey("BTBZMqHH6r4Tts7J_aSIgg"), as, mustKey("DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Errorf("encrypted =\n%s\nwant\n%s", b64.EncodeToString(got), want)
	}
}

// What the site encrypts, the phone (here, its key) can read back.
func TestPushEncryptRoundTrip(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	msg := []byte(`{"title":"Cancelled: Coffee Ride","body":"Rain"}`)
	body, err := encryptPush(msg, b64.EncodeToString(uaPriv.PublicKey().Bytes()), b64.EncodeToString(auth)+"==")
	if err != nil {
		t.Fatal(err)
	}
	if plain := openPush(t, uaPriv, auth, body); string(plain) != string(msg) {
		t.Errorf("decrypted %q", plain)
	}
}

// openPush is the phone's side of RFC 8291: it reads a message the site
// encrypted for it.
func openPush(t *testing.T, uaPriv *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	if rs != pushRecordSize || idlen != 65 {
		t.Fatalf("header: rs %d, idlen %d", rs, idlen)
	}
	asKey, _ := ecdh.P256().NewPublicKey(asPub)
	shared, _ := uaPriv.ECDH(asKey)
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPriv.PublicKey().Bytes())+string(asPub), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("no last-record mark: %q", plain)
	}
	return plain[:len(plain)-1]
}

// The VAPID token names the push service, expires, and is signed with the
// site's key; the header carries the public key browsers subscribed with.
func TestVAPIDAuth(t *testing.T) {
	ks, err := newVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := parseVAPIDKey(ks)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	h, err := vapidAuth(k, "https://fcm.googleapis.com/fcm/send/abc", "https://www.paxvelo.club", now)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := vapidPublic(k)
	if !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, ", k="+pub) {
		t.Fatalf("header = %q", h)
	}
	tok := strings.TrimSuffix(strings.TrimPrefix(h, "vapid t="), ", k="+pub)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", tok)
	}
	var claims struct {
		Aud string
		Exp int64
		Sub string
	}
	cb, _ := b64.DecodeString(parts[1])
	json.Unmarshal(cb, &claims)
	if claims.Aud != "https://fcm.googleapis.com" || claims.Sub != "https://www.paxvelo.club" || claims.Exp <= now.Unix() || claims.Exp > now.Add(24*time.Hour).Unix() {
		t.Errorf("claims = %+v", claims)
	}
	sig, _ := b64.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&k.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the token's signature doesn't check out")
	}
}
