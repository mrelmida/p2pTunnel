package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

const (
	// ALPNProtocol is the TLS ALPN identifier used for peer-to-peer QUIC tunnels.
	ALPNProtocol = "p2ptunnel-v1"
	// SignalingALPN is the TLS ALPN identifier used for client-to-server signaling.
	SignalingALPN = "p2ptunnel-sig-v1"
)

// Identity holds a peer's ephemeral Ed25519 TLS certificate and its SHA-256 DER fingerprint.
type Identity struct {
	PeerID      string
	Cert        tls.Certificate
	Fingerprint string
}

// GenerateIdentity creates a new ephemeral Ed25519 identity for a peer or server.
func GenerateIdentity() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate peer id: %w", err)
	}
	peerID := hex.EncodeToString(idBytes)

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	now := time.Now().Add(-5 * time.Minute)
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   peerID,
			Organization: []string{"p2pTunnel Ephemeral Identity"},
		},
		NotBefore:             now,
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("create x509 certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, fmt.Errorf("parse leaf certificate: %w", err)
	}

	fp := CertFingerprint(derBytes)

	return &Identity{
		PeerID: peerID,
		Cert: tls.Certificate{
			Certificate: [][]byte{derBytes},
			PrivateKey:  priv,
			Leaf:        leaf,
		},
		Fingerprint: fp,
	}, nil
}

// CertFingerprint returns the lowercase hex-encoded SHA-256 digest of a DER certificate.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// PeerServerTLSConfig returns a tls.Config for accepting incoming peer QUIC connections.
// It requires a client certificate whose SHA-256 fingerprint passes isAllowedFingerprint.
func (id *Identity) PeerServerTLSConfig(isAllowedFingerprint func(fp string) bool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{ALPNProtocol},
		MinVersion:   tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("missing peer client certificate")
			}
			fp := CertFingerprint(rawCerts[0])
			if isAllowedFingerprint != nil && !isAllowedFingerprint(fp) {
				return fmt.Errorf("untrusted peer certificate fingerprint: %s", fp)
			}
			return nil
		},
	}
}

// PeerClientTLSConfig returns a tls.Config for dialing a specific peer whose expected
// SHA-256 certificate fingerprint is known from the signaling server.
func (id *Identity) PeerClientTLSConfig(expectedFingerprint string) *tls.Config {
	expectedFingerprint = strings.ToLower(strings.TrimSpace(expectedFingerprint))
	return &tls.Config{
		Certificates:       []tls.Certificate{id.Cert},
		InsecureSkipVerify: true, // Verified explicitly via VerifyPeerCertificate below.
		NextProtos:         []string{ALPNProtocol},
		MinVersion:         tls.VersionTLS13,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("missing peer server certificate")
			}
			fp := CertFingerprint(rawCerts[0])
			if expectedFingerprint != "" && fp != expectedFingerprint {
				return fmt.Errorf("peer certificate fingerprint mismatch: got %s, want %s", fp, expectedFingerprint)
			}
			return nil
		},
	}
}

// SignalingServerTLSConfig returns the TLS config for the EU signaling TCP server.
func (id *Identity) SignalingServerTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Cert},
		NextProtos:   []string{SignalingALPN},
		MinVersion:   tls.VersionTLS13,
	}
}

// SignalingClientTLSConfig returns the TLS config for connecting to the EU signaling server.
func (id *Identity) SignalingClientTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{id.Cert},
		InsecureSkipVerify: true,
		NextProtos:         []string{SignalingALPN},
		MinVersion:         tls.VersionTLS13,
	}
}

// HashRoomPassword hashes an optional room password using domain-separated SHA-256.
func HashRoomPassword(password string) string {
	password = strings.TrimSpace(password)
	if password == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("p2ptunnel-room-auth-v1:" + password))
	return hex.EncodeToString(sum[:])
}

// HashServerSecret hashes the shared EU relay server secret using domain-separated SHA-256.
func HashServerSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("p2ptunnel-server-secret-v1:" + secret))
	return hex.EncodeToString(sum[:])
}
