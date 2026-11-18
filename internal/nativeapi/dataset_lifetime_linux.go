//go:build linux

package nativeapi

import (
	"context"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativequota"
)

// DatasetLifetime is bound before START to the exact native UUID, quota GUID and
// actual constructed parent. It has no user-assertion or persisted constructor.
type DatasetLifetime struct {
	life                           nativequota.DatasetLifetime
	client                         *Client
	domain, policy, name, resource string
}
type DatasetAbsenceObservation struct {
	identity                       nativequota.DatasetIdentityAbsence
	domain, policy, name, resource string
}

func (o DatasetAbsenceObservation) ResourceID() string                           { return o.resource }
func (o DatasetAbsenceObservation) DomainID() string                             { return o.domain }
func (o DatasetAbsenceObservation) PolicyDigest() string                         { return o.policy }
func (o DatasetAbsenceObservation) Name() string                                 { return o.name }
func (o DatasetAbsenceObservation) Identity() nativequota.DatasetIdentityAbsence { return o.identity }
func (c *Client) CaptureDatasetLifetime(ctx context.Context, p Prepared, q StoppedQuota, reader *nativequota.Reader) (DatasetLifetime, error) {
	if c == nil || c.capsule == nil || reader == nil || !c.matches(p) || !q.Valid() || q.domain != c.policy.DomainID() || q.policy != c.policy.Digest() || q.name != p.name || c.CheckBoundary(ctx) != nil {
		return DatasetLifetime{}, ErrNative
	}
	id, e := c.stopped(ctx, p)
	if e != nil || id != q.resource {
		return DatasetLifetime{}, ErrNative
	}
	dataset, e := c.dataset(ctx, p)
	if e != nil || dataset != q.Dataset() {
		return DatasetLifetime{}, ErrNative
	}
	record := c.capsule.Storage()
	root, e := reader.ReadFilesystem(ctx, record.Dataset)
	if e != nil || root.GUID() != record.GUID || root.Quota() != record.QuotaBytes {
		return DatasetLifetime{}, ErrNative
	}
	life, e := reader.CaptureLifetime(ctx, root, q.disk)
	if e != nil {
		return DatasetLifetime{}, ErrNative
	}
	id, e = c.stopped(ctx, p)
	if e != nil || id != q.resource || c.CheckBoundary(ctx) != nil {
		return DatasetLifetime{}, ErrNative
	}
	return DatasetLifetime{life, c, q.domain, q.policy, q.name, q.resource}, nil
}

// ObserveDatasetIdentityAbsence is NOT physical bytes-reclaimed evidence.
func (c *Client) ObserveDatasetIdentityAbsence(ctx context.Context, p Prepared, l DatasetLifetime) (DatasetAbsenceObservation, error) {
	if c == nil || l.client != c || l.domain != c.policy.DomainID() || l.policy != c.policy.Digest() || l.name != p.name || !uuid(l.resource) || !c.matches(p) || c.CheckBoundary(ctx) != nil {
		return DatasetAbsenceObservation{}, ErrNative
	}
	o, e := l.life.ObserveIdentityAbsence(ctx)
	if e != nil || c.CheckBoundary(ctx) != nil {
		return DatasetAbsenceObservation{}, ErrNative
	}
	return DatasetAbsenceObservation{o, l.domain, l.policy, l.name, l.resource}, nil
}
