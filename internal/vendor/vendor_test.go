package vendor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"ctyl/internal/policy"
)

// fixture writes a key file and returns a distribution trusting it.
func fixture(t *testing.T) (root string, d policy.Distribution, signer *openpgp.Entity) {
	t.Helper()
	root = t.TempDir()
	e, err := openpgp.NewEntity("Test Vendor", "", "vendor@example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if err := os.WriteFile(filepath.Join(root, "vendor.asc"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, policy.Distribution{Name: "test", SigningKey: "vendor.asc", SigningFingerprint: fmt.Sprintf("%X", e.PrimaryKey.Fingerprint)}, e
}

func sign(t *testing.T, e *openpgp.Entity, data []byte) []byte {
	t.Helper()
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, e, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	return sig.Bytes()
}

func TestSignatureVerification(t *testing.T) {
	root, d, signer := fixture(t)
	archive := filepath.Join(root, "jdk.tar.gz")
	data := []byte("pretend this is a JDK")
	os.WriteFile(archive, data, 0o644)

	got, err := SignatureCheck(root, d, archive, sign(t, signer, data))
	if err != nil || !strings.EqualFold(got, d.SigningFingerprint) {
		t.Fatalf("valid signature rejected: %v %s", err, got)
	}

	os.WriteFile(archive, append(data, '!'), 0o644)
	if _, err := SignatureCheck(root, d, archive, sign(t, signer, data)); err == nil {
		t.Error("tampered archive accepted")
	}

	other, _ := openpgp.NewEntity("Mirror", "", "mirror@example.invalid", nil)
	os.WriteFile(archive, data, 0o644)
	if _, err := SignatureCheck(root, d, archive, sign(t, other, data)); err == nil {
		t.Error("archive signed by an untrusted key accepted")
	}

	wrongPin := d
	wrongPin.SigningFingerprint = strings.Repeat("A", 40)
	if _, err := SignatureCheck(root, wrongPin, archive, sign(t, signer, data)); err == nil {
		t.Error("key file without the pinned fingerprint accepted")
	}
}

func TestPinnedVendorKeysMatchPolicy(t *testing.T) {
	p, err := policy.Load("../../policy")
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range p.Distributions {
		if d.SigningKey == "" {
			continue
		}
		if _, err := (&Fetcher{RootDir: "../.."}).keyring(d); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
