package localconfig

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	CertificateName = "server.crt"
	identityName    = "server.identity"
)

// EnsureIdentity is called under the data directory lock. The private identity
// is written first so a restart can publish the same public certificate.
func EnsureIdentity(dir string) (tls.Certificate, error) {
	identityPath := filepath.Join(dir, identityName)
	certPath := filepath.Join(dir, CertificateName)
	identity, err := readPrivateFile(identityPath)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(certPath); !errors.Is(err, os.ErrNotExist) {
			return tls.Certificate{}, errors.New("Buddy server identity is missing; restore it before starting the server")
		}
		// An HTTP-era bearer could have reached an impostor. Invalidate it
		// before publishing the new server identity.
		tokenPath := filepath.Join(dir, TokenName)
		if _, err := ReadToken(tokenPath); err == nil {
			if _, err := writeToken(tokenPath, true); err != nil {
				return tls.Certificate{}, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return tls.Certificate{}, err
		}
		identity, err = newIdentity()
		if err == nil {
			err = writePrivateFile(identityPath, identity, false)
		}
	}
	if err != nil {
		return tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(identity, identity)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	published, err := readPrivateFile(certPath)
	if errors.Is(err, os.ErrNotExist) {
		err = writePrivateFile(certPath, cert, false)
	} else if err == nil && !bytes.Equal(published, cert) {
		err = errors.New("Buddy's published certificate does not match its server identity")
	}
	return pair, err
}

func newIdentity() ([]byte, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Buddy local daemon"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"buddy.local"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
	}
	cert, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return nil, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...), nil
}

// ClientTLS pins the exact leaf certificate. The pin identifies the daemon
// across loopback addresses without trusting a system or user-installed CA.
func ClientTLS(dir string) (*tls.Config, error) {
	published, err := readPrivateFile(filepath.Join(dir, CertificateName))
	if err != nil {
		return nil, err
	}
	return PinnedTLS(published)
}

func PinnedTLS(published []byte) (*tls.Config, error) {
	block, rest := pem.Decode(published)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid Buddy server certificate")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(published) {
		return nil, errors.New("invalid Buddy server certificate")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: "buddy.local",
		RootCAs:    roots,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, block.Bytes) {
				return errors.New("Buddy server certificate does not match its saved identity")
			}
			return nil
		},
	}, nil
}

func writePrivateFile(path string, data []byte, replace bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".identity-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	if replace {
		return os.Rename(tmp.Name(), path)
	}
	return os.Link(tmp.Name(), path)
}

func IsLoopback(host string) bool {
	return net.ParseIP(host).IsLoopback()
}

func ValidateAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !IsLoopback(host) {
		return fmt.Errorf("use a loopback address such as %s", Address)
	}
	return nil
}
