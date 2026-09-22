package router_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	dtgo "github.com/dtcloudnow/dt-go/v26"
	"github.com/dtcloudnow/terraform-provider-dtcloud/dtcloud/internal/dterr"
)

// TestRouterNotFoundIsClassified pins that a missing router is recognised as
// missing rather than as a failed request.
//
// It matters more here than elsewhere: these endpoints hand the network layer's
// own error straight back and it carries no numeric code anywhere, so the
// classification rests entirely on the message text. Getting it wrong is not a
// failed apply but a silent one — a router deleted outside Terraform would block
// every plan.
//
// The bodies are copied from the live API, and the test drives the real SDK
// against them so it covers the whole path a 404 takes.
func TestRouterNotFoundIsClassified(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000000"

	bodies := map[string]string{
		"router":  `{"error":{"NeutronError":{"type":"RouterNotFound","message":"Router ` + id + ` could not be found","detail":""}},"code":"SERVER_ERROR"}`,
		"network": `{"error":{"NeutronError":{"type":"NetworkNotFound","message":"Network ` + id + ` could not be found","detail":""}},"code":"SERVER_ERROR"}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			client, err := dtgo.New(http.DefaultClient,
				dtgo.SetApiKey("test-access-key", "test-secret-key"),
				dtgo.SetBaseURL(server.URL))
			if err != nil {
				t.Fatalf("building the client: %s", err)
			}
			client.ServerId = "1"

			_, _, err = client.Router.GetRouterDetails(context.Background(), id, nil)
			if err == nil {
				t.Fatal("expected an error from a 404")
			}
			if !dterr.IsNotFound(err) {
				t.Fatalf("a missing router was not recognised as missing; the resource would fail a "+
					"read instead of being dropped from state.\nmessage: %s", err)
			}
		})
	}
}
