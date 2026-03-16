package secrets

import (
	"testing"
)

func TestPutAndGet(t *testing.T) {
	s := NewStore()
	err := s.Put("org/repo", "DB_PASSWORD", "super-secret-123", "admin")
	if err != nil {
		t.Fatal(err)
	}

	val, err := s.Get("org/repo", "DB_PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	if val != "super-secret-123" {
		t.Errorf("expected super-secret-123, got %s", val)
	}
}

func TestGetMissing(t *testing.T) {
	s := NewStore()
	_, err := s.Get("org/repo", "NOPE")
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestGetMultiple(t *testing.T) {
	s := NewStore()
	s.Put("org/repo", "A", "val-a", "admin")
	s.Put("org/repo", "B", "val-b", "admin")

	vals, err := s.GetMultiple("org/repo", []string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	if vals["A"] != "val-a" || vals["B"] != "val-b" {
		t.Errorf("unexpected values: %v", vals)
	}
}

func TestGetMultipleMissing(t *testing.T) {
	s := NewStore()
	s.Put("org/repo", "A", "val-a", "admin")

	_, err := s.GetMultiple("org/repo", []string{"A", "MISSING"})
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestScopeIsolation(t *testing.T) {
	s := NewStore()
	s.Put("org/repo-a", "SECRET", "value-a", "admin")
	s.Put("org/repo-b", "SECRET", "value-b", "admin")

	valA, _ := s.Get("org/repo-a", "SECRET")
	valB, _ := s.Get("org/repo-b", "SECRET")

	if valA != "value-a" {
		t.Errorf("expected value-a, got %s", valA)
	}
	if valB != "value-b" {
		t.Errorf("expected value-b, got %s", valB)
	}

	// Can't cross-read.
	_, err := s.Get("org/repo-a", "OTHER")
	if err == nil {
		t.Error("should not be able to read across scopes")
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()
	s.Put("scope", "KEY", "value", "admin")

	err := s.Delete("scope", "KEY")
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.Get("scope", "KEY")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestDeleteMissing(t *testing.T) {
	s := NewStore()
	err := s.Delete("nope", "KEY")
	if err == nil {
		t.Error("expected error for missing scope")
	}
}

func TestList(t *testing.T) {
	s := NewStore()
	s.Put("scope", "A", "secret-a", "admin")
	s.Put("scope", "B", "secret-b", "user1")

	entries := s.List("scope")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Values should NOT be in list output.
	for _, e := range entries {
		if e.Value != "" {
			t.Errorf("list should not expose values, got %q for %s", e.Value, e.Name)
		}
	}
}

func TestPutValidation(t *testing.T) {
	s := NewStore()

	if err := s.Put("", "KEY", "val", "admin"); err == nil {
		t.Error("expected error for empty scope")
	}
	if err := s.Put("scope", "", "val", "admin"); err == nil {
		t.Error("expected error for empty name")
	}
	if err := s.Put("scope", "KEY", "", "admin"); err == nil {
		t.Error("expected error for empty value")
	}
}

func TestScrubber(t *testing.T) {
	scrubber := NewScrubber([]string{
		"my-secret-token-12345",
		"another-password",
	})

	input := "connecting with token my-secret-token-12345 to server"
	result := scrubber.Scrub(input)

	if result != "connecting with token *** to server" {
		t.Errorf("expected scrubbed output, got: %s", result)
	}

	input2 := "password is another-password, don't leak it"
	result2 := scrubber.Scrub(input2)
	if result2 != "password is ***, don't leak it" {
		t.Errorf("expected scrubbed output, got: %s", result2)
	}
}

func TestScrubberAddValue(t *testing.T) {
	scrubber := NewScrubber(nil)
	scrubber.AddValue("new-secret")

	result := scrubber.Scrub("found new-secret in logs")
	if result != "found *** in logs" {
		t.Errorf("expected scrubbed output, got: %s", result)
	}
}

func TestScrubberShortValues(t *testing.T) {
	scrubber := NewScrubber([]string{"ab"}) // too short

	result := scrubber.Scrub("ab is everywhere: abc, ab, dab")
	// Should NOT scrub — too many false positives with 2-char secrets.
	if result != "ab is everywhere: abc, ab, dab" {
		t.Errorf("short values should not be scrubbed, got: %s", result)
	}
}

func TestScrubEnv(t *testing.T) {
	env := map[string]string{
		"HOME":         "/home/user",
		"DB_PASSWORD":  "secret123",
		"API_KEY":      "key456",
		"GITHUB_TOKEN": "ghp_xxx",
		"PATH":         "/usr/bin",
	}

	scrubbed := ScrubEnv(env)

	if scrubbed["HOME"] != "/home/user" {
		t.Error("HOME should not be scrubbed")
	}
	if scrubbed["PATH"] != "/usr/bin" {
		t.Error("PATH should not be scrubbed")
	}
	if scrubbed["DB_PASSWORD"] != "***" {
		t.Error("DB_PASSWORD should be scrubbed")
	}
	if scrubbed["API_KEY"] != "***" {
		t.Error("API_KEY should be scrubbed")
	}
	if scrubbed["GITHUB_TOKEN"] != "***" {
		t.Error("GITHUB_TOKEN should be scrubbed")
	}
}
