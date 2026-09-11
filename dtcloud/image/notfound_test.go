package image_test

import (
	"encoding/json"
	"testing"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
)

// TestImageNotFoundIsClassified is the rule that a missing image is recognised
// as missing rather than as a failed request.
//
// It matters more here than in the other services. Those answer a 404 with a
// structured body carrying a numeric code, which is the reliable half of
// dterr.IsNotFound. The images endpoints hand back the platform's own error,
// and the platform answers with an HTML page — so `error` is a *string*, there
// is no numeric code anywhere in the body, and the classification rests
// entirely on what that string happens to contain.
//
// The bodies below are copied from the live API rather than guessed. If the
// platform ever stops putting the status in its error page, this fails and the
// SDK has to start carrying the HTTP status code instead.
func TestImageNotFoundIsClassified(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000000"

	bodies := map[string]string{
		"details": `{"error":"<html>\n <head>\n  <title>404 Not Found</title>\n </head>\n <body>\n  <h1>404 Not Found</h1>\n  No image found with ID ` + id + `<br /><br />\n\n\n\n </body>\n</html>","code":"SERVER_ERROR"}`,
		"delete":  `{"error":"<html>\n <head>\n  <title>404 Not Found</title>\n </head>\n <body>\n  <h1>404 Not Found</h1>\n  Failed to find image ` + id + ` to delete<br /><br />\n\n\n\n </body>\n</html>","code":"SERVER_ERROR"}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			var parsed any
			if err := json.Unmarshal([]byte(body), &parsed); err != nil {
				t.Fatalf("the fixture is not valid JSON: %s", err)
			}
			err := &dtgo.ErrorResponse{Message: messageOf(parsed), RawBody: parsed}
			if !dterr.IsNotFound(err) {
				t.Fatalf("a missing image was not recognised as missing; the resource would fail a "+
					"read instead of being dropped from state.\nmessage: %s", err.Message)
			}
		})
	}
}

// messageOf mirrors how the SDK reduces a response body to a message: it
// follows the `error` key and stops at the first thing that is not an object.
func messageOf(body any) string {
	m, ok := body.(map[string]any)
	if !ok {
		s, _ := body.(string)
		return s
	}
	if inner, ok := m["error"]; ok {
		return messageOf(inner)
	}
	if msg, ok := m["message"].(string); ok {
		return msg
	}
	return ""
}
