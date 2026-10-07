package natsname

import "testing"

func TestSubject(t *testing.T) {
	for s, want := range map[string]bool{
		"orders.created": true, "a": true, "a-b_c.d1": true,
		"": false, "foo bar": false, "a\tb": false, "a..b": false, ".a": false, "a.": false,
		"orders.*": false, "orders.>": false,
	} {
		if got := Subject(s); got != want {
			t.Errorf("Subject(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestFilter(t *testing.T) {
	for s, want := range map[string]bool{
		"orders.created": true, "orders.*": true, "orders.>": true, "*.created": true, ">": true, "a.b*c": true,
		"": false, "foo bar": false, "foo..bar": false, "orders.>.x": false, "a.": false,
	} {
		if got := Filter(s); got != want {
			t.Errorf("Filter(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestName(t *testing.T) {
	for s, want := range map[string]bool{
		"ORDERS": true, "orders-v2": true, "verification_user": true,
		"": false, "bad.durable": false, "a b": false, "a*": false, "a>": false, "a/b": false, `a\b`: false, "a\nb": false,
	} {
		if got := Name(s); got != want {
			t.Errorf("Name(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestQueue(t *testing.T) {
	for s, want := range map[string]bool{"workers": true, "": true, "a.b": true, "a b": false, "a\tb": false} {
		if got := Queue(s); got != want {
			t.Errorf("Queue(%q) = %v, want %v", s, got, want)
		}
	}
}
