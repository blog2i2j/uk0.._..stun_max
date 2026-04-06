package core

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

// PeerCrypto holds the encryption state for a P2P peer connection.
// All methods are safe for concurrent use.
type PeerCrypto struct {
	PrivKey   *ecdh.PrivateKey
	PubKey    []byte // our public key (raw bytes)
	SharedKey []byte // derived 256-bit key

	// XChaCha20-Poly1305 AEAD — thread-safe for Seal/Open.
	// mu guards the nil → initialized transition of aead + encrypted.
	aead      *xchacha20AEAD
	encrypted bool
	mu        sync.RWMutex

	// Counter-based nonce — atomic, no lock needed.
	nonceCounter uint64
}

// Wrapper to hold the concrete type (avoids cipher.AEAD interface indirection).
type xchacha20AEAD struct {
	seal func(dst, nonce, plaintext, additionalData []byte) []byte
	open func(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	nonceSize int
	overhead  int
}

const (
	// xcNonceSize is 24 bytes for XChaCha20-Poly1305.
	xcNonceSize = chacha20poly1305.NonceSizeX // 24
	// xcOverhead is the Poly1305 tag (16 bytes).
	xcOverhead = chacha20poly1305.Overhead // 16
	// Per-packet overhead: 24-byte nonce + 16-byte tag = 40 bytes.
	CryptoOverhead = xcNonceSize + xcOverhead
)

// NewPeerCrypto generates a new X25519 key pair.
func NewPeerCrypto() (*PeerCrypto, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate X25519 key: %w", err)
	}
	// Random starting counter to avoid nonce reuse across sessions.
	var startCounter uint64
	b := make([]byte, 8)
	rand.Read(b)
	startCounter = binary.LittleEndian.Uint64(b)

	return &PeerCrypto{
		PrivKey:      priv,
		PubKey:       priv.PublicKey().Bytes(),
		nonceCounter: startCounter,
	}, nil
}

// DeriveKey performs X25519 ECDH and initializes XChaCha20-Poly1305.
func (pc *PeerCrypto) DeriveKey(peerPubKeyBytes []byte) error {
	peerPub, err := ecdh.X25519().NewPublicKey(peerPubKeyBytes)
	if err != nil {
		return fmt.Errorf("parse peer public key: %w", err)
	}

	shared, err := pc.PrivKey.ECDH(peerPub)
	if err != nil {
		return fmt.Errorf("ECDH: %w", err)
	}

	hash := sha256.Sum256(shared)

	aead, err := chacha20poly1305.NewX(hash[:])
	if err != nil {
		return fmt.Errorf("XChaCha20-Poly1305: %w", err)
	}

	pc.mu.Lock()
	pc.SharedKey = hash[:]
	pc.aead = &xchacha20AEAD{
		seal:      aead.Seal,
		open:      aead.Open,
		nonceSize: aead.NonceSize(),
		overhead:  aead.Overhead(),
	}
	pc.encrypted = true
	pc.mu.Unlock()
	return nil
}

// Encrypted returns whether encryption is active.
func (pc *PeerCrypto) IsEncrypted() bool {
	pc.mu.RLock()
	e := pc.encrypted
	pc.mu.RUnlock()
	return e
}

// nextNonce returns a unique 24-byte nonce using an atomic counter.
// Layout: [16-byte zero padding][8-byte little-endian counter]
func (pc *PeerCrypto) nextNonce() []byte {
	n := atomic.AddUint64(&pc.nonceCounter, 1)
	nonce := make([]byte, xcNonceSize)
	binary.LittleEndian.PutUint64(nonce[16:], n)
	return nonce
}

// Encrypt encrypts plaintext using XChaCha20-Poly1305 with counter nonce.
// Returns: [24-byte nonce][ciphertext + 16-byte Poly1305 tag]
func (pc *PeerCrypto) Encrypt(plaintext []byte) ([]byte, error) {
	pc.mu.RLock()
	a := pc.aead
	enc := pc.encrypted
	pc.mu.RUnlock()

	if !enc || a == nil {
		return plaintext, nil
	}

	nonce := pc.nextNonce()
	// Seal appends ciphertext+tag after nonce.
	ciphertext := a.seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt decrypts ciphertext using XChaCha20-Poly1305.
func (pc *PeerCrypto) Decrypt(data []byte) ([]byte, error) {
	pc.mu.RLock()
	a := pc.aead
	enc := pc.encrypted
	pc.mu.RUnlock()

	if !enc || a == nil {
		return data, nil
	}

	if len(data) < xcNonceSize {
		return nil, fmt.Errorf("ciphertext too short (%d bytes)", len(data))
	}

	nonce := data[:xcNonceSize]
	ciphertext := data[xcNonceSize:]
	plaintext, err := a.open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}
