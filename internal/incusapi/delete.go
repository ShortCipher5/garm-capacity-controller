package incusapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// Deletion is the one-shot destructive operation identity returned by Delete.
// A returned error after dispatch is unknown, never proof of absence: nothing
// here frees an allocation, claims the guest is gone or compensates.
type Deletion struct {
	OperationID string
	ResourceID  string
}

// Delete sends exactly one DELETE /1.0/instances/<name>?force=true against the
// pinned project. It never retries, issues compensating calls or converts a
// 404 into absence proof: every error stays an error and the caller's charge
// must remain until separate confirmed-absence observation.
func (c *Client) Delete(ctx context.Context, instanceName string) (Deletion, error) {
	if !name(instanceName) {
		return Deletion{}, ErrConfiguration
	}
	if err := c.trusted(ctx, false); err != nil {
		return Deletion{}, err
	}
	response, err := c.request(ctx, http.MethodDelete, "/1.0/instances/"+instanceName, url.Values{"force": {"true"}}, nil)
	if err != nil {
		return Deletion{}, err
	}
	if response.kind != "async" {
		return Deletion{}, ErrResponse
	}
	id, err := c.operationPath(response.operation)
	if err != nil {
		return Deletion{}, err
	}
	result := Deletion{OperationID: id, ResourceID: instanceName}
	metadata, err := object(response.metadata)
	if err != nil {
		return result, err
	}
	if raw, exists := metadata["id"]; exists {
		var returned string
		if json.Unmarshal(raw, &returned) != nil || returned != id {
			return result, ErrResponse
		}
	}
	return result, nil
}

// WaitDeletion polls GET /1.0/operations/<id> to a terminal status. It is the
// delete counterpart of Wait with the same strict-parse and no-retry
// conventions: success means only that the delete operation reported success.
// It performs no instance read and mints no absence proof; a 404, a single
// read or a successful DELETE alone never releases anything.
func (c *Client) WaitDeletion(ctx context.Context, deletion Deletion) error {
	if !operationID(deletion.OperationID) || !name(deletion.ResourceID) {
		return ErrConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	for {
		// Like Wait: each poll re-verifies the trusted standalone view; a
		// previous poll's identity observation is not a lease.
		if err := c.trusted(ctx, false); err != nil {
			return err
		}
		response, err := c.request(ctx, http.MethodGet, "/1.0/operations/"+deletion.OperationID, nil, nil)
		if err != nil {
			return err
		}
		if response.kind != "sync" {
			return ErrResponse
		}
		fields, err := object(response.metadata)
		if err != nil {
			return err
		}
		id, err := field[string](fields, "id")
		if err != nil || id != deletion.OperationID {
			return ErrResponse
		}
		resources, err := operationResources(fields)
		if err != nil || len(resources["instances"]) != 1 {
			return ErrResponse
		}
		instance, err := c.resourcePath(resources["instances"][0])
		if err != nil || instance != deletion.ResourceID {
			return ErrResponse
		}
		status, err := field[int](fields, "status_code")
		if err != nil {
			return ErrResponse
		}
		switch status {
		case 200:
			return nil
		case 100, 103, 104, 105:
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(ErrOperation, ctx.Err())
			case <-timer.C:
			}
		case 400, 401:
			return ErrOperation
		default:
			return ErrResponse
		}
	}
}
