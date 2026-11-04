package pages

import "testing"

func TestFoundingBadgeLabel(t *testing.T) {
	cases := map[int]string{
		0: "", -1: "", 1: "1st Founding Plant", 2: "2nd Founding Plant",
		3: "3rd Founding Plant", 4: "Founding Plant", 50: "Founding Plant",
	}
	for rank, want := range cases {
		if got := foundingBadgeLabel(rank); got != want {
			t.Errorf("foundingBadgeLabel(%d) = %q, want %q", rank, got, want)
		}
	}
}

func TestSpotsLeft(t *testing.T) {
	if spotsLeft(0, 50) != 50 || spotsLeft(49, 50) != 1 || spotsLeft(50, 50) != 0 || spotsLeft(60, 50) != 0 {
		t.Error("spotsLeft wrong")
	}
}
