package metrics

import "testing"

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"host.name", "host_name"},
		{"my-host", "my_host"},
		{"a.b-c.d", "a_b_c_d"},
	}
	for _, tt := range tests {
		if got := sanitize(tt.input); got != tt.want {
			t.Fatalf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGlobalNilBeforeInit(t *testing.T) {
	orig := global
	global = nil
	defer func() { global = orig }()

	if Global() != nil {
		t.Fatal("Global() = non-nil before Init, want nil")
	}
}

func TestWriteStatsFilesDoesNotPanic(t *testing.T) {
	// stats/ directory may not exist in test context; errors are silently
	// ignored by design — just verify this does not panic.
	WriteStatsFiles(10, 5, 1000)
}
