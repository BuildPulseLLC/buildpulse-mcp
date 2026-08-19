package mcpserver

import "testing"

func TestValidateOrganizationID(t *testing.T) {
	const uuid = "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"", false},
		{"  ", false},
		{uuid, false},
		{"  " + uuid + "  ", false},
		{"UUID-ALPHA", true},
		{"not-a-uuid", true},
		{uuid[:8], true},
	}
	for _, c := range cases {
		err := validateOrganizationID(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validateOrganizationID(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestValidateObjectID(t *testing.T) {
	got, err := validateObjectID("0123456789abcdef01234567", "test_id")
	if err != nil || got != "0123456789abcdef01234567" {
		t.Fatalf("valid id: got %q err %v", got, err)
	}
	got, err = validateObjectID("  0123456789ABCDEF01234567  ", "test_id")
	if err != nil || got != "0123456789abcdef01234567" {
		t.Fatalf("uppercase: got %q err %v", got, err)
	}
	if _, err := validateObjectID("", "test_id"); err == nil {
		t.Fatal("empty must error")
	}
	if _, err := validateObjectID("zzzzzzzzzzzzzzzzzzzzzzzz", "test_id"); err == nil {
		t.Fatal("non-hex must error")
	}
	if _, err := validateObjectID("0123456789abcdef0123456", "test_id"); err == nil {
		t.Fatal("short must error")
	}
}

func TestValidateRepoPart(t *testing.T) {
	ok := []string{"widgets", "acme", "my.repo", "org-name", "under_score"}
	for _, s := range ok {
		if _, err := validateRepoPart(s, "repository"); err != nil {
			t.Errorf("%q should be allowed: %v", s, err)
		}
	}
	bad := []string{"", "../etc", "acme/widgets", "foo\\bar", "user@host", "x\n y"}
	for _, s := range bad {
		if _, err := validateRepoPart(s, "repository"); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestValidateOptionalLimit(t *testing.T) {
	if err := validateOptionalLimit(0, 100); err != nil {
		t.Fatal(err)
	}
	if err := validateOptionalLimit(25, 100); err != nil {
		t.Fatal(err)
	}
	if err := validateOptionalLimit(101, 100); err == nil {
		t.Fatal("over max must error, not silently clamp")
	}
	if err := validateOptionalLimit(-1, 100); err == nil {
		t.Fatal("negative must error")
	}
}

func TestValidateHostedPlatformURL(t *testing.T) {
	ok := []string{
		"",
		"https://platform.buildpulse.io",
		"https://platform.buildpulse.io/",
		"https://platform.dev.buildpulse.io",
	}
	for _, s := range ok {
		if err := ValidateHostedPlatformURL(s); err != nil {
			t.Errorf("%q should be allowed: %v", s, err)
		}
	}
	bad := []string{
		"http://platform.buildpulse.io",
		"https://evil.example",
		"https://169.254.169.254",
		"https://127.0.0.1",
		"https://platform.buildpulse.io/extra",
		"https://user:pass@platform.buildpulse.io",
		"https://platform.buildpulse.io?x=1",
	}
	for _, s := range bad {
		if err := ValidateHostedPlatformURL(s); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}
