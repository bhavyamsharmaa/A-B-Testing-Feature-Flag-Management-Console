package events

import "testing"

// SDKs and /sdk/stream subscribe by environment UUID, and UUIDs did not
// change with multi-tenancy, so the live channel names must not either.
func TestFlagsChannelIsKeyedByEnvironmentUUID(t *testing.T) {
	const env = "3f2b6c1e-0000-4000-8000-000000000001"
	if got := FlagsChannel(env); got != "flags:"+env {
		t.Errorf("FlagsChannel = %q", got)
	}
	if FlagsChannel(env) == FlagsChannel("3f2b6c1e-0000-4000-8000-000000000002") {
		t.Error("different environments share a channel")
	}
}
