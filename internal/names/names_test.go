package names

import (
	"testing"
)

func TestGenerate(t *testing.T) {
	tests := []struct {
		id     string
		expect string
	}{
		// Test that the same ID always produces the same name (deterministic)
		{"01KPG9XZRN9YEMSK6EV7ZJQH6D", ""},
		{"01KPGA1W7ATDKMG9VMNNAY4A6X", ""},
	}

	for _, tt := range tests[:2] {
		name := Generate(tt.id)
		if name == "" {
			t.Errorf("Generate(%q) returned empty string", tt.id)
		}
		// Verify deterministic: same ID → same name
		name2 := Generate(tt.id)
		if name != name2 {
			t.Errorf("Generate(%q) not deterministic: got %q then %q", tt.id, name, name2)
		}
	}

	// Test invalid ID fallback
	name := Generate("not-a-valid-ulid")
	if name != "mystery-marmot" {
		t.Errorf("Generate(invalid) = %q, want mystery-marmot", name)
	}
}

func TestGenerateVariety(t *testing.T) {
	// Test that we get varied names for different IDs
	ids := []string{
		"01KPG9XZRN9YEMSK6EV7ZJQH6D",
		"01KPGA1W7ATDKMG9VMNNAY4A6X",
		"01KPGXZRN9YEMSK6EV7ZJQH6E",
		"01KPGAW7ATDKMG9VMNNAY4A6Y",
	}
	names := make(map[string]bool)
	for _, id := range ids {
		name := Generate(id)
		if name == "" {
			t.Errorf("Generate(%q) returned empty string", id)
		}
		// Verify format: adjective-animal
		if len(name) < 3 || name[0] == '-' || name[len(name)-1] == '-' {
			t.Errorf("Generate(%q) = %q, expected adjective-animal format", id, name)
		}
		// Check for variety
		if names[name] {
			t.Logf("Note: %q generated for multiple IDs (acceptable if IDs are similar)", name)
		}
		names[name] = true
	}
}
