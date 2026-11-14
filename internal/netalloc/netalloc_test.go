package netalloc

import (
	"errors"
	"net/netip"
	"testing"
)

func mustPool(t *testing.T, cidr string) Pool {
	t.Helper()
	p, err := NewPool(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAllocateSkipsReservedAndUsed(t *testing.T) {
	p := mustPool(t, "10.77.0.0/24")
	if p.Gateway != netip.MustParseAddr("10.77.0.1") {
		t.Fatalf("gateway = %s", p.Gateway)
	}
	got, err := p.Allocate(nil)
	if err != nil || got != netip.MustParseAddr("10.77.0.2") {
		t.Fatalf("first = %s, %v", got, err)
	}
	used := []netip.Addr{netip.MustParseAddr("10.77.0.2"), netip.MustParseAddr("10.77.0.4")}
	got, _ = p.Allocate(used)
	if got != netip.MustParseAddr("10.77.0.3") {
		t.Fatalf("expected the hole at .3, got %s", got)
	}
}

func TestAllocateExhaustion(t *testing.T) {
	p := mustPool(t, "192.168.5.0/29") // .1 gateway, .2-.6 usable, .7 broadcast
	var used []netip.Addr
	for i := 0; i < 5; i++ {
		a, err := p.Allocate(used)
		if err != nil {
			t.Fatalf("allocation %d: %v", i, err)
		}
		used = append(used, a)
	}
	if used[4] != netip.MustParseAddr("192.168.5.6") {
		t.Fatalf("last usable = %s", used[4])
	}
	if _, err := p.Allocate(used); !errors.Is(err, ErrExhausted) {
		t.Fatalf("want ErrExhausted, got %v", err)
	}
}

func TestNewPoolValidation(t *testing.T) {
	for _, bad := range []string{"nonsense", "fd00::/64", "10.0.0.0/31"} {
		if _, err := NewPool(bad); err == nil {
			t.Errorf("NewPool(%q) should fail", bad)
		}
	}
	if p := mustPool(t, "10.77.0.9/24"); p.Prefix.String() != "10.77.0.0/24" {
		t.Errorf("prefix not masked: %s", p.Prefix)
	}
}

func TestCIDRAndMAC(t *testing.T) {
	p := mustPool(t, "10.77.0.0/24")
	a := netip.MustParseAddr("10.77.0.42")
	if got := p.CIDR(a); got != "10.77.0.42/24" {
		t.Errorf("CIDR = %s", got)
	}
	if got := MAC(a); got != "02:73:62:4d:00:2a" {
		t.Errorf("MAC = %s", got)
	}
}

func TestTapName(t *testing.T) {
	if got := TapName("sb412"); got != "sbxt-sb412" {
		t.Errorf("TapName = %s", got)
	}
	if got := TapName("a-very-long-sandbox-name"); len(got) > 15 {
		t.Errorf("TapName too long: %q", got)
	}
}
