package proto

import "testing"

func TestFrameTypeValues(t *testing.T) {
	tests := []struct {
		ft   FrameType
		want uint8
	}{
		{FrameTypePing, 1},
		{FrameTypePong, 2},
		{FrameTypeError, 3},
	}
	for _, tt := range tests {
		if uint8(tt.ft) != tt.want {
			t.Errorf("FrameType %v = %d, want %d", tt.ft, uint8(tt.ft), tt.want)
		}
	}
}
