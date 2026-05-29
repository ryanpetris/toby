package fusekit

import "testing"

func TestFuseDebugEnabled(t *testing.T) {
	for _, value := range []string{"", "0", "false", "no", "off"} {
		t.Run("disabled_"+value, func(t *testing.T) {
			t.Setenv("TOBY_FUSE_DEBUG", value)
			if fuseDebugEnabled() {
				t.Fatalf("TOBY_FUSE_DEBUG=%q enabled debug", value)
			}
		})
	}
	for _, value := range []string{"1", "true", "yes", "on", "debug"} {
		t.Run("enabled_"+value, func(t *testing.T) {
			t.Setenv("TOBY_FUSE_DEBUG", value)
			if !fuseDebugEnabled() {
				t.Fatalf("TOBY_FUSE_DEBUG=%q disabled debug", value)
			}
		})
	}
}
