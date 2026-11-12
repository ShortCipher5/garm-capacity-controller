// Package securefiles reads owner-only regular configuration/key files without
// following a final symlink. The service owner and ancestor directories remain
// trusted; use separate OS identities for gateway and Garm.
package securefiles

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"syscall"
)

var ErrFile = errors.New("private configuration or credential file unavailable")

func Read(path string, maximum int64) ([]byte, error) {
	if path == "" || maximum <= 0 {
		return nil, ErrFile
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrFile
	}
	file := os.NewFile(uintptr(fd), "private-file")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maximum {
		return nil, ErrFile
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrFile
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrFile
	}
	return data, nil
}

type TLSFiles struct {
	CA          string `json:"ca"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

func LoadTLS(files TLSFiles) (*tls.Config, error) {
	ca, err := Read(files.CA, 1<<20)
	if err != nil {
		return nil, ErrFile
	}
	cert, err := Read(files.Certificate, 1<<20)
	if err != nil {
		return nil, ErrFile
	}
	key, err := Read(files.Key, 1<<20)
	if err != nil {
		return nil, ErrFile
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, ErrFile
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, ErrFile
	}
	return &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}, nil
}
