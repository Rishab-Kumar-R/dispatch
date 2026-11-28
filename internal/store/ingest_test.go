package store

import "testing"

func TestSameJSON(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":2}`, `{"b": 2, "a": 1}`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":1}`, `{"a":1.0}`, false},
		{`{"id":9007199254740993}`, `{"id":9007199254740992}`, false},
		{`[1,2]`, `[2,1]`, false},
		{`{"a":1}`, `not json`, false},
	}
	for _, tc := range cases {
		if got := sameJSON([]byte(tc.a), []byte(tc.b)); got != tc.want {
			t.Errorf("sameJSON(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
