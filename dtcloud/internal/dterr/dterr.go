// Package dterr interprets errors coming back from dt-go.
package dterr

import (
	"errors"
	"strings"

	dtgo "github.com/dtcloudnow/dt-go/v26"
)

// IsNotFound reports whether an error means "this resource does not exist". It
// only classifies — the caller keeps passing the API's own message through — and
// is used for one thing: dropping a resource from state instead of failing the
// run, which is what Terraform requires when something was deleted outside it.
//
// Two sources, in order of trust: the structured code in the response body,
// which dt-go keeps parsed on *dtgo.ErrorResponse.RawBody, and the message text
// for endpoints that answer in some other shape. Matching English prose is
// fragile, so it is deliberately the second choice.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}

	var resp *dtgo.ErrorResponse
	if errors.As(err, &resp) && hasStatusCode(resp.RawBody, 404) {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "404") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such") ||
		strings.Contains(msg, "could not be found")
}

// hasStatusCode walks a decoded JSON body looking for a numeric "code" equal to
// want. The interesting one is nested and the depth varies between endpoints, so
// the whole tree is searched. A string "code" is ignored.
func hasStatusCode(body any, want float64) bool {
	switch v := body.(type) {
	case map[string]any:
		for key, val := range v {
			if key == "code" {
				if n, ok := val.(float64); ok && n == want {
					return true
				}
			}
			if hasStatusCode(val, want) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasStatusCode(item, want) {
				return true
			}
		}
	}
	return false
}

func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "immutable") ||
		strings.Contains(msg, "pending_update") ||
		strings.Contains(msg, "pending_create") ||
		strings.Contains(msg, "pending_delete") ||
		strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "409")
}
