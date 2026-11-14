// Package netalloc assigns each sandbox an address on the host bridge.
//
// Allocation is derived from the sandboxes that already exist rather than
// a separate lease file, so it can never drift out of sync with them.
package netalloc

import (
	"errors"
	"fmt"
	"net/netip"
)

// Pool is an IPv4 subnet whose first host address is the bridge/gateway.
type Pool struct {
	Prefix  netip.Prefix
	Gateway netip.Addr
}

// ErrExhausted is returned when every address in the pool is taken.
var ErrExhausted = errors.New("netalloc: no free addresses")

// NewPool parses a CIDR such as "10.77.0.0/24".
func NewPool(cidr string) (Pool, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return Pool{}, err
	}
	p = p.Masked()
	if !p.Addr().Is4() {
		return Pool{}, fmt.Errorf("netalloc: %s is not IPv4", cidr)
	}
	if p.Bits() > 30 {
		return Pool{}, fmt.Errorf("netalloc: %s is too small", cidr)
	}
	return Pool{Prefix: p, Gateway: p.Addr().Next()}, nil
}

// broadcast returns the last address in the prefix.
func (p Pool) broadcast() netip.Addr {
	a := p.Prefix.Addr().As4()
	host := 32 - p.Prefix.Bits()
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v |= (1 << host) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// Allocate returns the lowest free address, skipping the network address,
// the gateway, the broadcast address, and everything in used.
func (p Pool) Allocate(used []netip.Addr) (netip.Addr, error) {
	taken := make(map[netip.Addr]bool, len(used))
	for _, u := range used {
		taken[u] = true
	}
	bc := p.broadcast()
	for a := p.Gateway.Next(); p.Prefix.Contains(a) && a != bc; a = a.Next() {
		if !taken[a] {
			return a, nil
		}
	}
	return netip.Addr{}, ErrExhausted
}

// CIDR formats addr with the pool's prefix length, e.g. "10.77.0.5/24".
func (p Pool) CIDR(addr netip.Addr) string {
	return netip.PrefixFrom(addr, p.Prefix.Bits()).String()
}

// MAC derives a stable, locally administered unicast MAC from an address.
// The 02:73:62 prefix spells "sb" after the local bit.
func MAC(addr netip.Addr) string {
	a := addr.As4()
	return fmt.Sprintf("02:73:62:%02x:%02x:%02x", a[1], a[2], a[3])
}

// TapName returns the host tap device name for a sandbox. Linux limits
// interface names to 15 bytes.
func TapName(id string) string {
	name := "sbxt-" + id
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}
