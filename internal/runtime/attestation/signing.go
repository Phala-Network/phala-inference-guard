package attestation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

const (
	AlgoECDSA   = "ecdsa"
	AlgoEd25519 = "ed25519"
)

type SigningContext struct {
	Algo         string
	Address      string
	AddressBytes []byte
	PublicKeyHex string
}

type Signers struct {
	ECDSA   SigningContext
	Ed25519 SigningContext
}

func NewSigners() (Signers, error) {
	ecdsaContext, err := newECDSAContext()
	if err != nil {
		return Signers{}, err
	}
	ed25519Context, err := newEd25519Context()
	if err != nil {
		return Signers{}, err
	}
	return Signers{ECDSA: ecdsaContext, Ed25519: ed25519Context}, nil
}

func (s Signers) Context(algo string) (SigningContext, bool) {
	switch algo {
	case "", AlgoECDSA:
		return s.ECDSA, true
	case AlgoEd25519:
		return s.Ed25519, true
	default:
		return SigningContext{}, false
	}
}

func newECDSAContext() (SigningContext, error) {
	privateKey, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return SigningContext{}, fmt.Errorf("generate ecdsa key: %w", err)
	}
	publicKey := privateKey.PubKey().SerializeUncompressed()
	if len(publicKey) != 65 || publicKey[0] != 0x04 {
		return SigningContext{}, fmt.Errorf("unexpected ecdsa public key length %d", len(publicKey))
	}
	rawPublicKey := publicKey[1:]
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write(rawPublicKey)
	sum := hasher.Sum(nil)
	addressBytes := sum[len(sum)-20:]
	return SigningContext{
		Algo:         AlgoECDSA,
		Address:      "0x" + hex.EncodeToString(addressBytes),
		AddressBytes: append([]byte(nil), addressBytes...),
		PublicKeyHex: hex.EncodeToString(rawPublicKey),
	}, nil
}

func newEd25519Context() (SigningContext, error) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return SigningContext{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return SigningContext{
		Algo:         AlgoEd25519,
		Address:      hex.EncodeToString(publicKey),
		AddressBytes: append([]byte(nil), publicKey...),
		PublicKeyHex: hex.EncodeToString(publicKey),
	}, nil
}
