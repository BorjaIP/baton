package proto

import "testing"

func TestNegotiate_CompatibleRangesPickHighestCommon(t *testing.T) {
	tests := []struct {
		name                   string
		cMin, cMax, sMin, sMax uint16
		want                   uint16
	}{
		{"exact single version match", 1, 1, 1, 1, 1},
		{"client narrower inside server range", 1, 1, 1, 3, 1},
		{"overlap picks highest common", 1, 3, 2, 4, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Negotiate(tt.cMin, tt.cMax, tt.sMin, tt.sMax)
			if !ok {
				t.Fatalf("Negotiate(%d,%d,%d,%d) ok=false, want true", tt.cMin, tt.cMax, tt.sMin, tt.sMax)
			}
			if got != tt.want {
				t.Fatalf("Negotiate(%d,%d,%d,%d) = %d, want %d", tt.cMin, tt.cMax, tt.sMin, tt.sMax, got, tt.want)
			}
		})
	}
}

func TestNegotiate_IncompatibleRangesReturnFalse(t *testing.T) {
	got, ok := Negotiate(99, 99, 1, 1)
	if ok {
		t.Fatalf("Negotiate(99,99,1,1) ok=true, want false (got version %d)", got)
	}
}

func TestNegotiate_ServerSupportsExactlyV1(t *testing.T) {
	got, ok := Negotiate(MinVersion, Version, MinVersion, Version)
	if !ok || got != Version {
		t.Fatalf("Negotiate with server defaults = (%d, %v), want (%d, true)", got, ok, Version)
	}
}
