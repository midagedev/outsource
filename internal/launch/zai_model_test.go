package launch

import (
	"strings"
	"testing"
)

// The z.ai endpoint accepts glm-5.2 without error but answers with glm-5.3
// (measured 2026-08-27, twice — see the zai row's silentMappings in
// wiring.go). FAIL-first: before the guard, `--model glm-5.2` launched, and on
// the crush harness (which has no model-identity assertion) the misassignment
// would have stayed silent forever; on claude-code it would burn the whole
// round before exiting 70.
func TestZaiMappedModelRefusedAtLaunch(t *testing.T) {
	zai, _ := findProvider("zai")
	msg, ok := mappedModelError(zai, "glm-5.2")
	if ok {
		t.Fatal("glm-5.2 on zai must be refused: it is measured to be answered by glm-5.3")
	}
	for _, want := range []string{"glm-5.2", "glm-5.3", "OUTSOURCE_ALLOW_MAPPED_MODEL"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal must name %q, got: %s", want, msg)
		}
	}

	// crush's provider-qualified form is the same model underneath.
	if _, ok := mappedModelError(zai, "zai/glm-5.2"); ok {
		t.Fatal("zai/glm-5.2 (crush form) must be refused like the bare id")
	}
}

func TestZaiMappedModelGuardScope(t *testing.T) {
	zai, _ := findProvider("zai")
	xai, _ := findProvider("xai")
	// Verbatim-honoured ids pass (measured 2026-08-27: glm-5.3,
	// glm-5.3-flash and glm-4.6 all answered as themselves).
	for _, m := range []string{"", "glm-5.3", "glm-5.3-flash", "glm-4.6", "zai/glm-5.3-flash"} {
		if msg, ok := mappedModelError(zai, m); !ok {
			t.Fatalf("model %q must pass the mapped-model guard, got: %s", m, msg)
		}
	}
	// The mapping is a per-provider column, so a provider that declares none
	// passes every id — the guard cannot leak across rows.
	if _, ok := mappedModelError(xai, "glm-5.2"); !ok {
		t.Fatal("a provider with no silentMappings must pass every id")
	}
}

// Vision is per (provider, model), not per provider: the zai default
// (glm-5.3) is blind, glm-5.3-flash sees (measured 2026-08-27 — "7" on the
// shape probe, #2244DD for a #1E50DC fill through the harness Read tool).
func TestModelVisionPerModel(t *testing.T) {
	zai, _ := findProvider("zai")
	xai, _ := findProvider("xai")
	cases := []struct {
		p     provider
		model string
		want  bool
	}{
		{zai, "", false},                 // default glm-5.3: blind
		{zai, "glm-5.3", false},          // measured: answered "Y" to a white 7
		{zai, "glm-5.3-flash", true},     // measured: answered "7"
		{zai, "zai/glm-5.3-flash", true}, // crush form, same model
		{xai, "", true},                  // provider-level vision still wins
	}
	for _, c := range cases {
		if got := modelVision(c.p, c.model); got != c.want {
			t.Fatalf("modelVision(%s, %q) = %v, want %v", c.p.name, c.model, got, c.want)
		}
	}
}

func TestZaiMappedModelOverrideForRemeasurement(t *testing.T) {
	t.Setenv("OUTSOURCE_ALLOW_MAPPED_MODEL", "1")
	zai, _ := findProvider("zai")
	if msg, ok := mappedModelError(zai, "glm-5.2"); !ok {
		t.Fatalf("override env must allow a re-measurement round, got: %s", msg)
	}
}
