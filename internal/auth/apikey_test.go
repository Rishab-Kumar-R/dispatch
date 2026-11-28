package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	raw, prefix, hash, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, keyPrefix) || !strings.HasPrefix(raw, prefix) || len(prefix) != len(keyPrefix)+4 {
		t.Fatalf("bad key %q / prefix %q", raw, prefix)
	}
	if !bytes.Equal(hash, HashKey(raw)) {
		t.Fatal("hash must match HashKey(raw)")
	}
	raw2, _, _, _ := GenerateKey()
	if raw == raw2 {
		t.Fatal("keys must be unique")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"  Bearer   abc  ", "abc", true},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"Basic abc", "", false},
		{"abc", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := BearerToken(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("BearerToken(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
