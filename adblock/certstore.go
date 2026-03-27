package adblock

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"
)

// CertStore 管理本地 CA 和为 MITM 签发的临时证书。
type CertStore struct {
	mu      sync.RWMutex
	caKey   *ecdsa.PrivateKey
	caCert  *x509.Certificate
	caDER   []byte // CA 证书 DER 编码，用于签发
	cache   map[string]*tls.Certificate
}

// NewCertStore 加载或生成本地 CA。
func NewCertStore(keyPath, certPath string) (*CertStore, error) {
	cs := &CertStore{
		cache: make(map[string]*tls.Certificate),
	}

	// 尝试加载已有 CA
	if keyPath != "" && certPath != "" {
		if err := cs.loadCA(keyPath, certPath); err == nil {
			return cs, nil
		}
	}

	// 生成新 CA
	if err := cs.generateCA(keyPath, certPath); err != nil {
		return nil, fmt.Errorf("generate CA: %w", err)
	}
	return cs, nil
}

// CertForHost 为指定域名签发临时证书。缓存有效期内的证书。
func (cs *CertStore) CertForHost(hostname string) (*tls.Certificate, error) {
	cs.mu.RLock()
	if cert, ok := cs.cache[hostname]; ok {
		cs.mu.RUnlock()
		return cert, nil
	}
	cs.mu.RUnlock()

	cs.mu.Lock()
	defer cs.mu.Unlock()

	// double check
	if cert, ok := cs.cache[hostname]; ok {
		return cert, nil
	}

	cert, err := cs.signHost(hostname)
	if err != nil {
		return nil, err
	}
	cs.cache[hostname] = cert
	return cert, nil
}

// CACertPEM 返回 CA 证书的 PEM 编码，用于导出给用户安装信任。
func (cs *CertStore) CACertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cs.caDER,
	})
}

func (cs *CertStore) loadCA(keyPath, certPath string) error {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to decode CA key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return fmt.Errorf("failed to decode CA cert PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return err
	}

	cs.caKey = key
	cs.caCert = cert
	cs.caDER = certBlock.Bytes
	return nil
}

func (cs *CertStore) generateCA(keyPath, certPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"AntiGravity Mesh"},
			CommonName:   "AntiGravity Local CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}

	cs.caKey = key
	cs.caCert = cert
	cs.caDER = der

	// 持久化到文件
	if keyPath != "" {
		keyDER, _ := x509.MarshalECPrivateKey(key)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
			return fmt.Errorf("write CA key: %w", err)
		}
	}
	if certPath != "" {
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
			return fmt.Errorf("write CA cert: %w", err)
		}
	}

	return nil
}

func (cs *CertStore) signHost(hostname string) (*tls.Certificate, error) {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		DNSNames:  []string{hostname},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, cs.caCert, &key.PublicKey, cs.caKey)
	if err != nil {
		return nil, err
	}

	cert := &tls.Certificate{
		Certificate: [][]byte{der, cs.caDER},
		PrivateKey:  key,
	}
	return cert, nil
}
