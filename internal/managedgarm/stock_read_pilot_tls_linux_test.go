//go:build stock_read_pilot_harness

package managedgarm

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"time"
)

// Fresh disposable material only, in memory. No CA private key is returned.
// Existing role DER is required, so separate key custody is actually checked.
type stockPilotTLSRoles struct {
	caDER, serverDER, serverPEM, serverKey, clientDER, clientPEM, clientKey, wrongDER, wrongPEM, wrongKey []byte
	stockDER                                                                                              []byte
	gatewaySPKI, ingressSPKI                                                                              string
}

func (r *stockPilotTLSRoles) clear() {
	if r == nil {
		return
	}
	for _, b := range [][]byte{r.caDER, r.serverDER, r.serverPEM, r.serverKey, r.clientDER, r.clientPEM, r.clientKey, r.wrongDER, r.wrongPEM, r.wrongKey, r.stockDER} {
		clear(b)
	}
	*r = stockPilotTLSRoles{}
}
func prepareStockPilotTLSRoles(now time.Time, stock, gateway, ingress []byte) (stockPilotTLSRoles, error) {
	var r stockPilotTLSRoles
	keys := map[string]bool{}
	for i, b := range [][]byte{stock, gateway, ingress} {
		if len(b) == 0 || len(b) > 16384 {
			return r, ErrBoundary
		}
		c, e := x509.ParseCertificate(b)
		if e != nil {
			return r, ErrBoundary
		}
		pin := stockBindingDigest(c.RawSubjectPublicKeyInfo)
		if keys[pin] {
			return r, ErrBoundary
		}
		keys[pin] = true
		if i == 1 {
			r.gatewaySPKI = pin
		}
		if i == 2 {
			r.ingressSPKI = pin
		}
	}
	if now.IsZero() {
		return stockPilotTLSRoles{}, ErrBoundary
	}
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return stockPilotTLSRoles{}, ErrBoundary
	}
	defer clear(key)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	r.caDER, e = x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if e != nil {
		return stockPilotTLSRoles{}, ErrBoundary
	}
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte, []byte, error) {
		pub, k, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, nil, nil, e
		}
		defer clear(k)
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		if usage == x509.ExtKeyUsageServerAuth {
			c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, e := x509.CreateCertificate(rand.Reader, c, ca, pub, key)
		if e != nil {
			return nil, nil, nil, e
		}
		pk, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			return nil, nil, nil, e
		}
		defer clear(pk)
		return der, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), nil
	}
	fail := func() (stockPilotTLSRoles, error) { r.clear(); return stockPilotTLSRoles{}, ErrBoundary }
	r.serverDER, r.serverPEM, r.serverKey, e = issue(2, x509.ExtKeyUsageServerAuth)
	if e != nil {
		return fail()
	}
	r.clientDER, r.clientPEM, r.clientKey, e = issue(3, x509.ExtKeyUsageClientAuth)
	if e != nil {
		return fail()
	}
	r.wrongDER, r.wrongPEM, r.wrongKey, e = issue(4, x509.ExtKeyUsageClientAuth)
	if e != nil {
		return fail()
	}
	for _, b := range [][]byte{r.caDER, r.serverDER, r.clientDER, r.wrongDER} {
		c, e := x509.ParseCertificate(b)
		if e != nil {
			return fail()
		}
		pin := stockBindingDigest(c.RawSubjectPublicKeyInfo)
		if keys[pin] {
			return fail()
		}
		keys[pin] = true
	}
	r.stockDER = append([]byte(nil), stock...)
	return r, nil
}
func (r stockPilotTLSRoles) serveMaterial(auth stockReadAuthBinding) (stockReadServeMaterial, error) {
	review := stockReadServeReview{Version: "stock-read-serve/v1", GarmID: auth.review.GarmID, ControllerID: auth.record.ControllerID, CASHA256: stockBindingDigest(r.caDER), ServerSHA256: stockBindingDigest(r.serverDER), ClientSHA256: stockBindingDigest(r.clientDER), GatewaySPKI: r.gatewaySPKI, IngressSPKI: r.ingressSPKI}
	raw, e := json.Marshal(review)
	if e != nil {
		return stockReadServeMaterial{}, ErrBoundary
	}
	return validateStockReadServeMaterial([5][]byte{raw, r.caDER, r.serverPEM, r.serverKey, r.clientDER}, auth, r.stockDER)
}
