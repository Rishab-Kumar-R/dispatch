package delivery

import (
	"errors"
	"net/netip"
	"testing"
)

func TestCheckAddr(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},
		{"127.0.0.1", true},
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"100.64.0.1", true},
		{"0.0.0.0", true},
		{"224.0.0.1", true},
		{"255.255.255.255", true},
		{"::1", true},
		{"fe80::1", true},
		{"fd00:ec2::254", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"64:ff9b::7f00:1", true},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			err := CheckAddr(netip.MustParseAddr(tc.ip))
			if tc.blocked && !errors.Is(err, ErrBlockedDestination) {
				t.Fatalf("expected %s to be blocked, got %v", tc.ip, err)
			}
			if !tc.blocked && err != nil {
				t.Fatalf("expected %s to be allowed, got %v", tc.ip, err)
			}
		})
	}
}

func TestValidateEndpointURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://example.com/hooks", true},
		{"https://example.com:8443/hooks?x=1", true},
		{"http://example.com/hooks", false},
		{"ftp://example.com", false},
		{"https://user:pass@example.com/", false},
		{"https:///nohost", false},
		{"https://example.com:99999/", false},
		{"https://127.0.0.1/", false},
		{"https://10.1.2.3/hook", false},
		{"https://[::1]/hook", false},
		{"https://169.254.169.254/latest/meta-data", false},
		{"", false},
	}
	for _, tc := range cases {
		err := ValidateEndpointURL(tc.url)
		if tc.ok && err != nil {
			t.Errorf("%q: unexpected error %v", tc.url, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q: expected an error", tc.url)
		}
	}
}
