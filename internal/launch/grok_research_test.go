package launch

import (
	"strings"
	"testing"
)

// A research round's notice names its done marker as the final message's last
// line, so the model does not look for a file to write it to.
func TestResearchMarkerNotice(t *testing.T) {
	got := researchMarkerNotice("DONE-MAP")
	if !strings.Contains(got, "`DONE-MAP`") || !strings.Contains(got, "마지막 줄") {
		t.Fatalf("the notice does not name the marker as the last line: %q", got)
	}
	if strings.Contains(researchMarkerNotice(""), "DONE") {
		t.Fatalf("a round without a marker is told about one: %q", researchMarkerNotice(""))
	}
}
