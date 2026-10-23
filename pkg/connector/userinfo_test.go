package connector

import "testing"

func TestNumberMayRename(t *testing.T) {
	cases := []struct {
		current, phone string
		want           bool
	}{
		{"", "+79648986389", true},
		{"+79648986389 (WA)", "+79648986389", true},
		{"+79648986389", "+79648986389", true},
		{"Вячеслав", "+79648986389", false},
		{"Вячеслав (WA)", "+79648986389", false},
		{"Anna", "", false},
	}
	for _, c := range cases {
		if got := numberMayRename(c.current, c.phone); got != c.want {
			t.Errorf("numberMayRename(%q, %q) = %v, want %v", c.current, c.phone, got, c.want)
		}
	}
}
