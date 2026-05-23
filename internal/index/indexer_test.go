package index

import (
	"testing"
	"time"
)

func TestParseSegmentTime(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantOK   bool
		wantTime time.Time
	}{
		{
			name:     "standard format",
			filename: "2024-03-15_14-32-01.fmp4",
			wantOK:   true,
			wantTime: time.Date(2024, 3, 15, 14, 32, 1, 0, time.Local),
		},
		{
			name:     "with microseconds suffix",
			filename: "2024-03-15_14-32-01-123456.fmp4",
			wantOK:   true,
			wantTime: time.Date(2024, 3, 15, 14, 32, 1, 0, time.Local),
		},
		{
			name:     "mp4 extension",
			filename: "2024-03-15_14-32-01.mp4",
			wantOK:   true,
			wantTime: time.Date(2024, 3, 15, 14, 32, 1, 0, time.Local),
		},
		{
			name:     "unrecognised format",
			filename: "random_filename.fmp4",
			wantOK:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseSegmentTime(tc.filename)
			if ok != tc.wantOK {
				t.Errorf("ok: got %v, want %v", ok, tc.wantOK)
				return
			}
			if ok && !got.Equal(tc.wantTime) {
				t.Errorf("time: got %v, want %v", got, tc.wantTime)
			}
		})
	}
}
