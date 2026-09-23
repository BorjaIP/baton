package proto

import "testing"

func TestValidateScope_Table(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		pattern string
		wantErr bool
	}{
		{"valid absolute root and relative pattern", "/home/user/repo", "src/auth/**", false},
		{"valid wildcard pattern", "/home/user/repo", "**", false},
		{"empty root rejected", "", "src/**", true},
		{"empty pattern rejected", "/home/user/repo", "", true},
		{"non-absolute root rejected", "home/user/repo", "src/**", true},
		{"non-clean root rejected", "/home/user/../repo", "src/**", true},
		{"absolute pattern rejected", "/home/user/repo", "/etc/passwd", true},
		{"dot pattern rejected", "/home/user/repo", ".", true},
		{"dotdot pattern rejected", "/home/user/repo", "..", true},
		{"dotdot-prefixed pattern rejected", "/home/user/repo", "../escape/**", true},
		{"pattern with NUL rejected", "/home/user/repo", "src/\x00/**", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateScope(tt.root, tt.pattern)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateScope(%q, %q) = nil, want error", tt.root, tt.pattern)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateScope(%q, %q) = %v, want nil", tt.root, tt.pattern, err)
			}
		})
	}
}
