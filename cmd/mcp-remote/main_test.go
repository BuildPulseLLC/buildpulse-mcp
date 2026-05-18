package main

import "testing"

func TestExtractToken(t *testing.T) {
	const legacyHex = "0123456789abcdef0123456789abcdef01234567"
	const bpToken = "bp_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	cases := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		// Header parsing — we still require a recognized scheme + non-empty credential.
		{"empty", "", "", true},
		{"missing token", "Bearer", "", true},
		{"empty credential", "Bearer ", "", true},
		{"wrong scheme", "Basic " + legacyHex, "", true},

		// Shape-agnostic pass-through — platform-api owns token-format validation,
		// so the MCP edge accepts whatever the caller sends.
		{"bearer legacy 40-hex", "Bearer " + legacyHex, legacyHex, false},
		{"bearer different case", "bearer " + legacyHex, legacyHex, false},
		{"legacy token scheme", "token " + legacyHex, legacyHex, false},
		{"bearer bp_ token", "Bearer " + bpToken, bpToken, false},
		{"token scheme bp_ token", "token " + bpToken, bpToken, false},
		{"trailing whitespace", "Bearer  " + legacyHex + "  ", legacyHex, false},
		// Even shapes platform-api will reject (uppercase, too short) are
		// forwarded — we let platform-api be the authority on what's valid.
		{"uppercase token forwarded", "Bearer ABCDEF0123456789ABCDEF0123456789ABCDEF01", "ABCDEF0123456789ABCDEF0123456789ABCDEF01", false},
		{"short token forwarded", "Bearer abc", "abc", false},
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
