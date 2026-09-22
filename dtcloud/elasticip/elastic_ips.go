// Package elasticip implements the dtcloud_elastic_ip resource and its data
// sources. An elastic IP is the API's floating IP.
//
// Association is an attribute of the address rather than a separate endpoint,
// and the update is all-or-nothing: a body with no port_id disassociates. So the
// association lives on this resource.
//
// The resource reads the details endpoint, which round-trips; the list endpoint
// reports a display shape, which is what the data sources expose. DOWN is a
// resting state, so the waiters compare port_id.
package elasticip

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/wait"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
)

// Floating IP statuses.
const (
	statusActive = "ACTIVE"
	statusDown   = "DOWN"
	statusError  = "ERROR"
)

// deref renders a nullable API string as the empty string. An unassociated
// address reports null for three fields, which must clear the stored value.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// formatTime renders an API timestamp for state. An absent or unparsable one
// reads as the empty string rather than as a misleading zero date.
func formatTime(t dtgo.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// portDetails is what the address points at, decoded from the raw body: dt-go
// types the field as *string while the API sends an object.
type portDetails struct {
	DeviceID    string
	DeviceOwner string
}

func parsePortDetails(body string) portDetails {
	var parsed struct {
		PortDetails *struct {
			DeviceID    string `json:"device_id"`
			DeviceOwner string `json:"device_owner"`
		} `json:"port_details"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil || parsed.PortDetails == nil {
		return portDetails{}
	}
	return portDetails{DeviceID: parsed.PortDetails.DeviceID, DeviceOwner: parsed.PortDetails.DeviceOwner}
}

// waitForAssociation blocks until the address reports the requested port and
// the matching status; an empty port means "not associated". Both are needed,
// since a status-only wait can return before anything moves.
func waitForAssociation(ctx context.Context, client *dtgo.Client, id, wantPort string, timeout time.Duration) error {
	wantStatus := statusActive
	if wantPort == "" {
		wantStatus = statusDown
	}

	stateConf := &retry.StateChangeConf{
		Pending: []string{"waiting"},
		Target:  []string{"done"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.FloatingIps.GetDetails(ctx, id, nil)
			if err != nil {
				// A freshly created address can 404 briefly; keep polling rather than
				// failing the whole apply.
				if dterr.IsNotFound(err) {
					return "waiting", "waiting", nil
				}
				return nil, "", err
			}
			if details == nil || details.ID == "" {
				return "waiting", "waiting", nil
			}
			if details.Status == statusError {
				return nil, "", fmt.Errorf("elastic IP %s entered ERROR state", id)
			}
			if deref(details.PortID) != wantPort || details.Status != wantStatus {
				return "waiting", "waiting", nil
			}
			// The router is torn down with the association and is the last thing to
			// go, so it is the honest end of a disassociate.
			if wantPort == "" && deref(details.RouterID) != "" {
				return "waiting", "waiting", nil
			}
			return "done", "done", nil
		},
		Timeout:    timeout,
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(2 * time.Second),
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}

// waitForGone blocks until the address stops resolving, so that destroy does
// not return while the platform is still releasing it.
func waitForGone(ctx context.Context, client *dtgo.Client, id string, timeout time.Duration) error {
	stateConf := &retry.StateChangeConf{
		Pending: []string{"present"},
		Target:  []string{"gone"},
		Refresh: func() (interface{}, string, error) {
			details, _, err := client.FloatingIps.GetDetails(ctx, id, nil)
			if err != nil {
				if dterr.IsNotFound(err) {
					return "gone", "gone", nil
				}
				return nil, "", err
			}
			if details == nil || details.ID == "" {
				return "gone", "gone", nil
			}
			return "present", "present", nil
		},
		Timeout:    timeout,
		Delay:      wait.Pace(2 * time.Second),
		MinTimeout: wait.Pace(2 * time.Second),
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}
