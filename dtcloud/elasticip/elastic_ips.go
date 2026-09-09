// Package elasticip implements the dtcloud_elastic_ip resource and its data
// sources.
//
// An elastic IP — a floating IP in the API's own vocabulary, and in OpenStack's
// — is a public address drawn from an external network that can be pointed at a
// port and moved between resources without being released.
//
// # Why the association lives on this resource, and not on its own
//
// The obvious alternative, and the one the security group service took, is a
// second resource for the association. It is wrong here, and the reason is in
// the API: there is no attach endpoint. Association is an *attribute* of the
// floating IP, changed by `PUT /floatingips/{id}`, and that PUT is
// all-or-nothing — a body with no `port_id` does not leave the association
// alone, it **removes** it. So two resources both writing `port_id` through the
// same call would overwrite each other on every apply. One resource, one
// writer.
//
// (terraform-provider-openstack and terraform-provider-digitalocean both ship
// an IP resource *and* a separate association resource, and both then warn you
// not to use them together. That warning is the design smell.)
//
// Moving the address between machines is still an in-place update, which is the
// thing that matters: the address is the valuable part and it is never released
// to change what it points at.
//
// # Two read endpoints, and this time the raw one is `details`
//
//	GET /floatingips/{id}/details  ->  raw Neutron, round-trips
//	GET /floatingips               ->  cooked for the panel
//
// The list resolves the external network to a *name*, works out whether the
// address is on a VM or a load balancer and names that too, and drops
// everything else. It costs the API three extra calls to build. The resource
// therefore reads `details`; the cooked view is what the data sources are for.
//
// # Status
//
// A floating IP is DOWN when it is not associated and ACTIVE when it is —
// DOWN is a normal resting state, not a failure. The platform's own websocket
// waits for DOWN after a create and ACTIVE after an associate. The waiters here
// watch `port_id` instead, because that is the value that actually changed;
// waiting on status alone is the mistake this codebase has now made three times
// elsewhere.
package elasticip

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dtgo "github.com/dtcloudnow/dt-go"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/retry"
)

// Floating IP statuses reported by Neutron.
const (
	statusActive = "ACTIVE"
	statusDown   = "DOWN"
	statusError  = "ERROR"
)

// deref renders a nullable API string. Neutron sends null for every field of an
// unassociated address — port_id, fixed_ip_address, router_id — and null is not
// the same as absent, so each becomes the empty string rather than being left
// at whatever was in state.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// formatTime renders an API timestamp for state. A timestamp that was absent,
// null or in a format dtgo could not parse reads as the empty string rather
// than a misleading zero date.
func formatTime(t dtgo.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// portDetails is what the address is currently pointed at, dug out of the raw
// response body.
//
// It is read from the body rather than from the typed struct because dt-go
// declares GetFloatingIpDetails.PortDetails as *string, while Neutron sends an
// object. Decoding an object into a string is a type error; Go's decoder skips
// that one field and carries on, and dt-go discards the error — so the typed
// field is silently nil for exactly the addresses that have something in it.
//
// Reading the raw body sidesteps that without a change to dt-go, the same way
// the VM service recovers hotPlugEnabled. The dt-go fix is a one-liner and is
// on the list to raise.
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

// waitForAssociation blocks until the address has *finished* moving to the
// association it was asked for — the empty port meaning "not associated".
//
// The condition is the port the caller asked for *and* the status that goes
// with it. Watching the status alone would be the mistake this codebase shipped
// on VM resize and again on volume extend: both operations are accepted while
// the address is already settled, so a status-only wait can return having
// confirmed nothing.
//
// In practice the platform answers synchronously — the PUT reply already
// carries the new association, and eight consecutive reads after it agree — so
// this usually returns on the first poll. It is here for the create path, which
// the platform's own websocket does treat as asynchronous, and for the day that
// changes.
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
				// A freshly created address can 404 briefly; keep polling
				// rather than failing the whole apply.
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
			// The router is torn down with the association and is the last
			// thing to go, so it is the honest end of a disassociate.
			if wantPort == "" && deref(details.RouterID) != "" {
				return "waiting", "waiting", nil
			}
			return "done", "done", nil
		},
		Timeout:    timeout,
		Delay:      2 * time.Second,
		MinTimeout: 2 * time.Second,
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
		Delay:      2 * time.Second,
		MinTimeout: 2 * time.Second,
	}
	_, err := stateConf.WaitForStateContext(ctx)
	return err
}
