package loadbalancer

import "testing"

// TestBareID pins the rule that a child resource may be pointed at a sibling by
// either form of its id. A sibling's Terraform id carries its parents' in front
// of it, and the endpoints refuse anything but the bare UUID -- so writing the
// natural `listener_id = dtcloud_lb_listener.x.id` used to reach the platform as
// "<lb-id>:<listener-id>" and come back as "Value should be UUID format".
func TestBareID(t *testing.T) {
	cases := map[string]string{
		"bare":                "1cebc935-48b6-41cd-8682-d3f624def0b6",
		"listener composite":  "b131958f-64b1-44c7-9c89-5290eecce5a6:1cebc935-48b6-41cd-8682-d3f624def0b6",
		"member composite":    "b131958f:e06630b5:23268573",
		"empty":               "",
		"trailing separator":  "b131958f:",
		"no separator at all": "plain",
	}
	want := map[string]string{
		"bare":                "1cebc935-48b6-41cd-8682-d3f624def0b6",
		"listener composite":  "1cebc935-48b6-41cd-8682-d3f624def0b6",
		"member composite":    "23268573",
		"empty":               "",
		"trailing separator":  "",
		"no separator at all": "plain",
	}
	for name, in := range cases {
		if got := bareID(in); got != want[name] {
			t.Errorf("%s: bareID(%q) = %q, want %q", name, in, got, want[name])
		}
	}
}
