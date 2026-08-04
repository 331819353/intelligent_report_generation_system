package auth

import "testing"

func TestStrongPassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{name: "valid", password: "DataSource9!", want: true},
		{name: "too short", password: "Source9!", want: false},
		{name: "missing uppercase", password: "datasource9!", want: false},
		{name: "missing lowercase", password: "DATASOURCE9!", want: false},
		{name: "missing digit", password: "DataSources!", want: false},
		{name: "space rejected", password: "Data Source9!", want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := strongPassword(test.password); got != test.want {
				t.Fatalf("strongPassword(%q)=%v, want %v", test.password, got, test.want)
			}
		})
	}
}
