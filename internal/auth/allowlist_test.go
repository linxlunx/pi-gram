package auth

import "testing"

func TestAllowlist(t *testing.T) {
	a := New([]int64{1, 2, 9999999999})
	if !a.Allowed(1) || !a.Allowed(2) || !a.Allowed(9999999999) {
		t.Fatal("allowed users rejected")
	}
	if a.Allowed(3) || a.Allowed(0) || a.Allowed(-1) {
		t.Fatal("unknown users accepted")
	}
	if a.Size() != 3 {
		t.Fatalf("size = %d, want 3", a.Size())
	}
}

func TestAllowlistEmpty(t *testing.T) {
	a := New(nil)
	if a.Allowed(1) {
		t.Fatal("empty allowlist must not authorize anyone")
	}
}
