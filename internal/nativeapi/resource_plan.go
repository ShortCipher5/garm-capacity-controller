package nativeapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/ShortCipher5/garm-capacity-controller/internal/nativeegress"
	"sort"
	"strings"
)

// ResourcePlan compiles fixed fresh-resource requests; it is NOT evidence that
// a dataset is fresh, a namespace is isolated, or any operation is authorized.
// No execution method exists. The future executor must consume actual constructed
// namespace/dataset birth handles and a durable first-attempt before each POST.
type ResourcePlan struct {
	domain, policy, parent, digest string
	uplink                         bool
	requests                       []ResourceRequest
}
type ResourceRequest struct{ Method, Path, Body string }

func (p ResourcePlan) Digest() string { return p.digest }
func (p ResourcePlan) Requests() []ResourceRequest {
	return append([]ResourceRequest(nil), p.requests...)
}

// PlanFreshResources requires each immutable pool source to be one direct child
// of the reviewed quota parent. Incus may delete its source on failed creation;
// giving it the quota parent or runtime root would therefore be unsafe. Input
// strings are only planning data, never proof of ownership/freshness or quota.
// Bridges are deliberately isolated L2: no DHCP/DNS, NAT, uplink, host attachment,
// or implied runner Internet connectivity. Egress needs a separately enforced
// namespace construction contract before an executor may use this plan.
func (p Policy) PlanFreshResources(quotaParent string) (ResourcePlan, error) {
	if p.spec.EgressDigest != "" {
		return ResourcePlan{}, ErrPolicy
	}
	return p.planFreshResources(quotaParent, nil)
}
func (p Policy) planFreshResources(quotaParent string, network *nativeegress.Policy) (ResourcePlan, error) {
	if !hash(p.digest) || !resourceDataset(quotaParent) {
		return ResourcePlan{}, ErrPolicy
	}
	pools := map[string]string{}
	datasets := map[string]string{}
	bridges := map[string]bool{}
	for _, v := range p.spec.Pools {
		if strings.TrimPrefix(v.ZFSDataset, quotaParent+"/") == "runtime" || !resourceDataset(v.ZFSDataset) || !strings.HasPrefix(v.ZFSDataset, quotaParent+"/") || strings.Contains(strings.TrimPrefix(v.ZFSDataset, quotaParent+"/"), "/") {
			return ResourcePlan{}, ErrPolicy
		}
		if old, ok := pools[v.StoragePool]; ok && old != v.ZFSDataset {
			return ResourcePlan{}, ErrPolicy
		}
		if old, ok := datasets[v.ZFSDataset]; ok && old != v.StoragePool {
			return ResourcePlan{}, ErrPolicy
		}
		// Kernel bridge interface names cannot exceed IFNAMSIZ-1.
		if len(v.Bridge) > 15 || v.Bridge == "lo" || v.Bridge == "none" {
			return ResourcePlan{}, ErrPolicy
		}
		pools[v.StoragePool] = v.ZFSDataset
		datasets[v.ZFSDataset] = v.StoragePool
		bridges[v.Bridge] = true
	}
	if network != nil && ((p.spec.Version != EgressPolicyVersion && p.spec.Version != PartitionPolicyVersion) || p.spec.EgressDigest != network.Digest() || len(bridges) != 1) {
		return ResourcePlan{}, ErrPolicy
	}
	out := ResourcePlan{uplink: network != nil, domain: p.spec.DomainID, policy: p.digest, parent: quotaParent}
	names := make([]string, 0, len(pools))
	for n := range pools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body, _ := json.Marshal(struct {
			Name   string            `json:"name"`
			Driver string            `json:"driver"`
			Config map[string]string `json:"config"`
		}{n, "zfs", map[string]string{"source": pools[n], "zfs.pool_name": pools[n], "zfs.export": "false"}})
		out.requests = append(out.requests, ResourceRequest{"POST", "/1.0/storage-pools", string(body)})
	}
	names = names[:0]
	for n := range bridges {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		config := map[string]string{"ipv4.address": "none", "ipv6.address": "none", "ipv4.nat": "false", "ipv6.nat": "false", "dns.mode": "none", "bridge.driver": "native"}
		if network != nil {
			if n == "garm0" {
				return ResourcePlan{}, ErrPolicy
			}
			config["ipv4.address"] = network.RunnerGateway()
			config["ipv4.nat"] = "true"
			config["ipv4.nat.address"] = network.GuestAddress()
			config["ipv4.routing"] = "true"
			config["ipv4.firewall"] = "true"
			config["ipv4.dhcp"] = "true"
			config["dns.nameservers"] = strings.Join(network.DNS(), ",")
		}
		body, _ := json.Marshal(struct {
			Name   string            `json:"name"`
			Type   string            `json:"type"`
			Config map[string]string `json:"config"`
		}{n, "bridge", config})
		out.requests = append(out.requests, ResourceRequest{"POST", "/1.0/networks", string(body)})
	}
	raw, _ := json.Marshal(struct {
		Version, Domain, Policy, Parent string
		Requests                        []ResourceRequest
	}{"fresh-resource-plan/v1", out.domain, out.policy, out.parent, out.requests})
	sum := sha256.Sum256(raw)
	out.digest = hex.EncodeToString(sum[:])
	return out, nil
}
func resourceDataset(s string) bool {
	if len(s) == 0 || len(s) > 255 || !strings.Contains(s, "/") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for i, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '-' || r == '.')) {
				return false
			}
		}
	}
	return true
}
