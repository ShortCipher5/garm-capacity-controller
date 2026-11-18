package jobobserver

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
)

func freezeTLS(in *tls.Config) (*tls.Config, error) {
	if in == nil || in.RootCAs == nil || in.GetClientCertificate != nil || len(in.Certificates) != 1 {
		return nil, ErrConfiguration
	}
	out := in.Clone()
	out.RootCAs = in.RootCAs.Clone()
	if in.ClientCAs != nil {
		out.ClientCAs = in.ClientCAs.Clone()
	}
	out.Certificates = make([]tls.Certificate, len(in.Certificates))
	for i, cert := range in.Certificates {
		copied := cert
		copied.Certificate = make([][]byte, len(cert.Certificate))
		for j, der := range cert.Certificate {
			copied.Certificate[j] = bytes.Clone(der)
		}
		copied.OCSPStaple = bytes.Clone(cert.OCSPStaple)
		copied.SignedCertificateTimestamps = make([][]byte, len(cert.SignedCertificateTimestamps))
		for j, timestamp := range cert.SignedCertificateTimestamps {
			copied.SignedCertificateTimestamps[j] = bytes.Clone(timestamp)
		}
		copied.SupportedSignatureAlgorithms = append([]tls.SignatureScheme(nil), cert.SupportedSignatureAlgorithms...)
		copied.Leaf = nil
		if len(copied.Certificate) == 0 {
			return nil, ErrConfiguration
		}
		leaf, e := x509.ParseCertificate(copied.Certificate[0])
		if e != nil {
			return nil, ErrConfiguration
		}
		copied.Leaf = leaf
		keyBytes, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
		if e != nil {
			return nil, ErrConfiguration
		}
		key, e := x509.ParsePKCS8PrivateKey(keyBytes)
		clear(keyBytes)
		if e != nil {
			return nil, ErrConfiguration
		}
		copied.PrivateKey = key
		out.Certificates[i] = copied
	}
	out.NameToCertificate = nil
	out.NextProtos = append([]string(nil), in.NextProtos...)
	out.CipherSuites = append([]uint16(nil), in.CipherSuites...)
	out.CurvePreferences = append([]tls.CurveID(nil), in.CurvePreferences...)
	out.ClientSessionCache = nil
	out.KeyLogWriter = nil
	return out, nil
}
