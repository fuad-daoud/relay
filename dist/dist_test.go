package dist

import (
	"strings"
	"testing"
)

// TestClientUnitIsTheServiceFile pins that the embedded unit is the one
// `make service` installs: relevo's own daemon line, with the %h the user unit
// expands.
func TestClientUnitIsTheServiceFile(t *testing.T) {
	if !strings.Contains(ClientUnit, "ExecStart=%h/.local/bin/relevo daemon") {
		t.Errorf("ClientUnit does not name relevo's daemon ExecStart:\n%s", ClientUnit)
	}
}

// TestLaunchdPlistKeepsItsPlaceholders pins that the template is embedded
// unrendered: migrate substitutes @BIN@ and @HOME@, so both must survive in
// the embedded bytes.
func TestLaunchdPlistKeepsItsPlaceholders(t *testing.T) {
	if !strings.Contains(LaunchdPlist, "@BIN@") {
		t.Errorf("LaunchdPlist does not contain @BIN@:\n%s", LaunchdPlist)
	}
	if !strings.Contains(LaunchdPlist, "@HOME@") {
		t.Errorf("LaunchdPlist does not contain @HOME@:\n%s", LaunchdPlist)
	}
}
