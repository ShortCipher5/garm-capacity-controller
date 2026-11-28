package incusapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MetricsObserver is a read-only transport for a dedicated Incus metrics
// credential. It has no instance configuration, operations, or provisioning
// methods. The server must enforce the certificate's project restrictions.
type MetricsObserver struct {
	endpoint url.URL
	http     *http.Client
}

// GuestTelemetry is display-only running-container data. CPU is the
// cumulative sum of user and system seconds, not a utilization rate.
type GuestTelemetry struct {
	Name            string   `json:"name"`
	CPUUsedSeconds  *float64 `json:"cpu_used_seconds,omitempty"`
	MemoryUsedBytes *uint64  `json:"memory_used_bytes,omitempty"`
}

const (
	cpuSeries       = "incus_cpu_seconds_total"
	memTotalSeries  = "incus_memory_MemTotal_bytes"
	memFreeSeries   = "incus_memory_MemFree_bytes"
	maxMetricGuests = 10000
)

var metricLabel = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)="((?:[^"\\]|\\[n"\\])*)"`)

func metricLabels(raw string) (map[string]string, bool) {
	out := map[string]string{}
	for len(raw) != 0 && len(out) < 32 {
		m := metricLabel.FindStringSubmatch(raw)
		if m == nil {
			return nil, false
		}
		if _, exists := out[m[1]]; exists {
			return nil, false
		}
		x, err := strconv.Unquote("\"" + m[2] + "\"")
		if err != nil {
			return nil, false
		}
		out[m[1]] = x
		raw = raw[len(m[0]):]
		if raw == "" {
			return out, true
		}
		if raw[0] != ',' || len(raw) == 1 {
			return nil, false
		}
		raw = raw[1:]
	}
	return nil, false
}

func parseGuestTelemetry(data []byte, project string) ([]GuestTelemetry, error) {
	type observation struct {
		cpu map[string]float64
		mem map[string]uint64
	}
	byName := map[string]*observation{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		family, rest, ok := strings.Cut(scanner.Text(), "{")
		if !ok || (family != cpuSeries && family != memTotalSeries && family != memFreeSeries) {
			continue
		}
		// Incus emits metric samples without timestamps. Find the outer
		// brace from the end so an unrelated label can't truncate the row.
		idx := strings.LastIndexByte(rest, '}')
		if idx < 0 {
			return nil, ErrResponse
		}
		labels, ok := metricLabels(rest[:idx])
		if !ok || labels["project"] != project || !name(labels["name"]) {
			return nil, ErrResponse
		}
		if labels["type"] != "container" {
			continue
		}
		fields := strings.Fields(rest[idx+1:])
		if len(fields) != 1 {
			return nil, ErrResponse
		}
		x, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
			return nil, ErrResponse
		}
		guest := labels["name"]
		row := byName[guest]
		if row == nil {
			if len(byName) >= maxMetricGuests {
				return nil, ErrResponse
			}
			row = &observation{cpu: map[string]float64{}, mem: map[string]uint64{}}
			byName[guest] = row
		}
		if family == cpuSeries {
			mode := labels["mode"]
			if mode != "user" && mode != "system" {
				continue
			}
			cpu := labels["cpu"]
			if _, err := strconv.ParseUint(cpu, 10, 16); err != nil {
				return nil, ErrResponse
			}
			key := cpu + "/" + mode
			if _, exists := row.cpu[key]; exists {
				return nil, ErrResponse
			}
			row.cpu[key] = x
		} else {
			// Do not round fractional values or exceed exactly representable
			// integers when Incus encodes byte counts as a float.
			if math.Trunc(x) != x || x >= 1<<53 {
				return nil, ErrResponse
			}
			if _, exists := row.mem[family]; exists {
				return nil, ErrResponse
			}
			row.mem[family] = uint64(x)
		}
	}
	if scanner.Err() != nil {
		return nil, ErrResponse
	}
	result := make([]GuestTelemetry, 0, len(byName))
	for name, row := range byName {
		t := GuestTelemetry{Name: name}
		sum, complete := 0.0, len(row.cpu) > 0
		for key, x := range row.cpu {
			cpu, mode, _ := strings.Cut(key, "/")
			other := "user"
			if mode == "user" {
				other = "system"
			}
			_, pair := row.cpu[cpu+"/"+other]
			complete = complete && pair
			sum += x
		}
		if complete && !math.IsInf(sum, 0) {
			t.CPUUsedSeconds = &sum
		}
		total, haveTotal := row.mem[memTotalSeries]
		free, haveFree := row.mem[memFreeSeries]
		if haveTotal && haveFree && total > 0 && free <= total {
			used := total - free
			t.MemoryUsedBytes = &used
		}
		if t.CPUUsedSeconds != nil || t.MemoryUsedBytes != nil {
			result = append(result, t)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func NewMetricsObserver(endpoint, project, serverKeyPin string, credentials *tls.Config) (*MetricsObserver, error) {
	trust, err := pinnedObserverTLS(serverKeyPin, credentials)
	if err != nil {
		return nil, err
	}
	// Reuse the normal verified native TLS policy, but expose only one route
	// and do not request native root metadata (unavailable to metrics roles).
	client, err := New(endpoint, project, trust)
	if err != nil {
		return nil, err
	}
	target := client.base
	target.Path = "/1.0/metrics"
	target.RawQuery = url.Values{"project": {project}}.Encode()
	return &MetricsObserver{endpoint: target, http: client.http}, nil
}

func (m *MetricsObserver) Close() { m.http.CloseIdleConnections() }

// ReadGuestTelemetry returns observed running-container usage only. Missing
// series do not identify stopped guests, empty capacity or admission headroom.
func (m *MetricsObserver) ReadGuestTelemetry(ctx context.Context) ([]GuestTelemetry, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint.String(), nil)
	if err != nil {
		return nil, ErrConfiguration
	}
	request.Header.Set("Accept", "text/plain; version=0.0.4")
	response, err := m.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrTransport
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrTransport
	}
	contentType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "text/plain" || (params["version"] != "" && params["version"] != "0.0.4") {
		return nil, ErrResponse
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil || len(data) > maxBody || ctx.Err() != nil {
		return nil, ErrResponse
	}
	return parseGuestTelemetry(data, m.endpoint.Query().Get("project"))
}
