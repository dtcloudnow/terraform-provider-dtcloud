// Package dterr interprets errors coming back from dt-go.
package dterr

import (
	"errors"
	"strings"

	dtgo "github.com/dtcloudnow/dt-go"
)

// IsNotFound reports whether an error means "this resource does not exist".
//
// It never rewrites or swallows the API's own message: callers keep passing the
// error through with %s, so whatever cloud-web-api said is what the user reads.
// This is only a classifier, and it is used for one thing — deciding to drop a
// resource from Terraform state instead of failing the run, which is what
// Terraform requires when something was deleted outside of it.
//
// Two sources, in order of trust:
//
//  1. The structured code in the response body. A real 404 looks like
//     {"error":{"itemNotFound":{"code":404,"message":"Keypair x not found ..."}},
//     "code":"SERVER_ERROR"} — dt-go keeps that parsed body on
//     *dtgo.ErrorResponse.RawBody, so the 404 can be read rather than guessed.
//
//  2. The message text, as a fallback for endpoints that answer in some other
//     shape. Matching English prose is fragile — the day the API rewords or
//     localises a message, a missing resource starts looking like a real
//     failure — so it is deliberately the second choice, not the first.
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
// want. The interesting one is nested (error.itemNotFound.code) and the depth
// varies between endpoints, so the whole tree is searched rather than one path.
// A string "code" — the body also carries "code":"SERVER_ERROR" — is ignored.
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
