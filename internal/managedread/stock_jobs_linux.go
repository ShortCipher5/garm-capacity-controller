package managedread

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
)

const stockJobPageSize = 100
const stockJobPageMax = 16
const stockJobTotalBytes = 4 << 20

// fetchJobs preserves the old reader's array contract and aggregate byte
// budget. v0.2.2 paginates webhook jobs and excludes completed jobs by
// default. Never return a silently truncated or observably shifting list.
func (s *isolatedStockReads) fetchJobs(ctx context.Context, token string) ([]byte, error) {
	probe, err := s.get(ctx, "/api/v1/jobs", token)
	if err != nil {
		return nil, err
	}
	probe = bytes.TrimSpace(probe)
	if len(probe) == 0 {
		return nil, ErrConfiguration
	}
	if probe[0] == '[' {
		return probe, nil
	}
	if probe[0] != '{' || !json.Valid(probe) {
		return nil, ErrConfiguration
	}
	type pageResponse struct {
		Total    *int              `json:"total_count"`
		Pages    *int              `json:"pages"`
		Current  *int              `json:"current_page"`
		Next     *int              `json:"next_page"`
		Previous *int              `json:"previous_page"`
		Results  []json.RawMessage `json:"results"`
	}
	total := -1
	var previousID int64
	count := 0
	var out bytes.Buffer
	out.WriteByte('[')
	for page := 1; page <= stockJobPageMax; page++ {
		path := "/api/v1/jobs?all=true&page=" + strconv.Itoa(page) + "&pageSize=" + strconv.Itoa(stockJobPageSize)
		raw, err := s.get(ctx, path, token)
		if err != nil {
			return nil, err
		}
		var p pageResponse
		if json.Unmarshal(raw, &p) != nil || p.Total == nil || p.Pages == nil || p.Current == nil ||
			p.Results == nil || *p.Current != page || *p.Total < 0 || *p.Total > stockJobPageSize*stockJobPageMax {
			return nil, ErrConfiguration
		}
		if total == -1 {
			total = *p.Total
		}
		pages := (total + stockJobPageSize - 1) / stockJobPageSize
		want := min(stockJobPageSize, total-count)
		more := page < pages
		if total != *p.Total || *p.Pages != pages || len(p.Results) != want ||
			(more && (p.Next == nil || *p.Next != page+1)) || (!more && p.Next != nil) ||
			(page > 1 && (p.Previous == nil || *p.Previous != page-1)) || (page == 1 && p.Previous != nil) {
			return nil, ErrConfiguration
		}
		for _, record := range p.Results {
			var id struct {
				ID int64 `json:"id"`
			}
			if json.Unmarshal(record, &id) != nil || id.ID <= 0 || (count > 0 && id.ID >= previousID) ||
				out.Len()+len(record)+2 > stockJobTotalBytes {
				return nil, ErrConfiguration
			}
			if count > 0 {
				out.WriteByte(',')
			}
			out.Write(record)
			previousID = id.ID
			count++
		}
		if !more {
			out.WriteByte(']')
			return out.Bytes(), nil
		}
	}
	return nil, ErrConfiguration
}
