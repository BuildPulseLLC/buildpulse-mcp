package main

import "testing"

func TestExtractToken(t *testing.T) {
	const validHex = "0123456789abcdef0123456789abcdef01234567"

	cases := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{"bearer lowercase", "Bearer " + validHex, validHex, false},
		{"bearer different case", "bearer " + validHex, validHex, false},
		{"legacy token scheme", "token " + validHex, validHex, false},
		{"empty", "", "", true},
		{"missing token", "Bearer", "", true},
		{"uppercase token", "Bearer " + "ABCDEF0123456789ABCDEF0123456789ABCDEF01", "", true},
		{"too short", "Bearer " + "abc", "", true},
		{"wrong scheme", "Basic " + validHex, "", true},
		{"trailing whitespace", "Bearer  " + validHex + "  ", validHex, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractToken(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Errorf("extractToken(%q) = %q, want error", tc.header, got)
				}
				return
			}
			if err != nil {
				t.Errorf("extractToken(%q) unexpected err: %v", tc.header, err)
				return
			}
			if got != tc.want {
				t.Errorf("extractToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
