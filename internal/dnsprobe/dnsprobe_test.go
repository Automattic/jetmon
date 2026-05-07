package dnsprobe

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

type fakeResolver struct {
	addrs    []net.IPAddr
	cname    string
	cnameErr error
	err      error
}

func (r fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addrs, r.err
}

func (r fakeResolver) LookupCNAME(context.Context, string) (string, error) {
	if r.cnameErr != nil {
		return "", r.cnameErr
	}
	if r.cname == "" {
		return "", &net.DNSError{Err: "no such host", IsNotFound: true}
	}
	return r.cname, nil
}

func TestCheckWithResolverSuccessNormalizesEvidence(t *testing.T) {
	res := CheckWithResolver(context.Background(), fakeResolver{
		addrs: []net.IPAddr{
			{IP: net.ParseIP("2001:db8::1")},
			{IP: net.ParseIP("192.0.2.10")},
			{IP: net.ParseIP("192.0.2.10")},
		},
		cname: "Origin.Example.COM.",
	}, Request{BlogID: 42, Hostname: "WWW.Example.COM.", Timeout: time.Second})

	if !res.Success || res.Status != StatusOK {
		t.Fatalf("result = %+v, want success ok", res)
	}
	wantAddrs := []string{"192.0.2.10", "2001:db8::1"}
	if fmt.Sprint(res.Addresses) != fmt.Sprint(wantAddrs) {
		t.Fatalf("addresses = %v, want %v", res.Addresses, wantAddrs)
	}
	if got := fmt.Sprint(res.CNAMEChain); got != "[origin.example.com]" {
		t.Fatalf("CNAMEChain = %s", got)
	}
}

func TestCheckWithResolverPreservesCNAMEEvidenceOnAddressFailure(t *testing.T) {
	res := CheckWithResolver(context.Background(), fakeResolver{
		cname: "Target.Example.COM.",
		err:   &net.DNSError{Err: "no such host", IsNotFound: true},
	}, Request{BlogID: 42, Hostname: "Alias.Example.COM.", Timeout: time.Second})

	if res.Success || res.Status != StatusNXDomain {
		t.Fatalf("result = %+v, want nxdomain failure", res)
	}
	if got := fmt.Sprint(res.CNAMEChain); got != "[target.example.com]" {
		t.Fatalf("CNAMEChain = %s, want target evidence", got)
	}
}

func TestCheckWithResolverClassifiesDNSErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: &net.DNSError{Err: "no such host", IsNotFound: true}, want: StatusNXDomain},
		{name: "timeout", err: &net.DNSError{Err: "timeout", IsTimeout: true}, want: StatusTimeout},
		{name: "temporary", err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}, want: StatusSERVFAIL},
		{name: "generic", err: fmt.Errorf("resolver failed"), want: StatusResolverError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := CheckWithResolver(context.Background(), fakeResolver{err: tt.err}, Request{Hostname: "example.com", Timeout: time.Second})
			if res.Success || res.Status != tt.want {
				t.Fatalf("status = %q success=%t, want %q false", res.Status, res.Success, tt.want)
			}
		})
	}
}

func TestCheckWithResolverRejectsEmptyHostname(t *testing.T) {
	res := CheckWithResolver(context.Background(), fakeResolver{}, Request{})
	if res.Success || res.Status != StatusInvalidHost {
		t.Fatalf("result = %+v, want invalid host failure", res)
	}
}

func TestNormalizeResolverAddrsAddsPortsAndDeduplicates(t *testing.T) {
	got := normalizeResolverAddrs([]string{"1.1.1.1", "1.1.1.1:53", "[2001:db8::1]", ""})
	want := []string{"1.1.1.1:53", "[2001:db8::1]:53"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("normalizeResolverAddrs = %v, want %v", got, want)
	}
}

func TestResolverForRequestUsesConfiguredResolver(t *testing.T) {
	_, label := resolverForRequest(Request{
		BlogID:        42,
		Hostname:      "example.com",
		ResolverAddrs: []string{"192.0.2.53"},
	})
	if label != "192.0.2.53:53" {
		t.Fatalf("resolver label = %q, want configured resolver", label)
	}
}
