package archkit

import (
	"testing"
)

func TestWallClock(t *testing.T) {
	t.Parallel()
	const file = "internal/domain/order/wait.go"
	for _, fn := range []string{"Now", "Since", "Until", "After", "AfterFunc", "Tick", "NewTimer", "NewTicker", "Sleep"} {
		t.Run(fn, func(t *testing.T) {
			t.Parallel()
			src := "package order\n\nimport \"time\"\n\nvar wait = time." + fn + "\n"
			requireFailures(t, failures(t, edits{file: src}), []string{file + " uses time." + fn + ": " + whyClock})
		})
	}

	runCases(t, []ruleCase{
		{
			name: "alias in module",
			edits: edits{"internal/domain/order/module/clock.go": `package module

import stdtime "time"

var now = stdtime.Now
`},
			want: []string{"internal/domain/order/module/clock.go uses time.Now: " + whyClock},
		},
		{
			name: "dot import",
			edits: edits{file: `package order

import . "time"

func stamp() Time { return Now() }
`},
			want: []string{file + " dot-imports time: " + whyDotClock},
		},
		{
			name:  "dot import next to another one",
			edits: edits{file: "package order\n\nimport (\n\t. `time`\n\t. \"strings\"\n)\n\nfunc stamp() Time { return Now().Add(Duration(Count(\"a\", \"a\"))) }\n"},
			want:  []string{file + " dot-imports time: " + whyDotClock},
		},
		{
			name: "every package of a domain",
			edits: edits{
				"internal/domain/order/infra/postgres/clock.go": "package postgres\n\nimport \"time\"\n\nfunc now() time.Time { return time.Now() }\n",
				"internal/domain/order/delivery/http/clock.go":  "package http\n\nimport \"time\"\n\nfunc wait() { time.Sleep(time.Second) }\n",
			},
			want: []string{
				"internal/domain/order/delivery/http/clock.go uses time.Sleep: " + whyClock,
				"internal/domain/order/infra/postgres/clock.go uses time.Now: " + whyClock,
			},
		},
		{
			name: "one line per function of a file",
			edits: edits{file: `package order

import "time"

func age(t time.Time) time.Duration {
	if time.Now().After(t) {
		return time.Since(t)
	}
	return time.Now().Sub(t)
}
`},
			want: []string{file + " uses time.Now: " + whyClock, file + " uses time.Since: " + whyClock},
		},
		{
			name: "time values",
			edits: edits{file: `package order

import "time"

var (
	epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	day   = 24 * time.Hour
	parse = time.Parse
)

func expires(at time.Time, ttl time.Duration) time.Time { return at.Add(ttl).Truncate(time.Second) }

func after(deadline, at time.Time) bool { return at.After(deadline) }
`},
		},
		{
			name: "clock of the domain",
			edits: edits{file: `package order

import (
	"time"

	"` + kit + `/clockkit"
)

func stamp(clock clockkit.Clock, uc *Usecase) (time.Time, <-chan time.Time) {
	return clock.Now(), uc.d.Clock.After(time.Second)
}
`},
		},
		{
			name: "package named time",
			edits: edits{file: `package order

import time "` + mod + `/pkg/paging"

var now = time.Now
`},
		},
		{
			name: "tests and code outside domains",
			edits: edits{
				"internal/domain/order/wait_test.go": "package order\n\nimport \"time\"\n\nvar started = time.Now()\n",
				"internal/domain/order/dot_test.go":  "package order\n\nimport . \"time\"\n\nvar begun = Now()\n",
				"internal/platform/identity/at.go":   "package identity\n\nimport \"time\"\n\nvar started = time.Now()\n",
				"internal/platform/identity/dot.go":  "package identity\n\nimport . \"time\"\n\nvar begun = Now()\n",
				"pkg/paging/at.go":                   "package paging\n\nimport \"time\"\n\nvar started = time.Now()\n",
				"cmd/api/at.go":                      "package main\n\nimport \"time\"\n\nvar started = time.Now()\n",
				"internal/testkit/orderfix/at.go":    "package orderfix\n\nimport \"time\"\n\nvar started = time.Now()\n",
			},
		},
	})
}

func TestIdentityClosures(t *testing.T) {
	t.Parallel()
	const file = "internal/domain/order/module/ports.go"
	ports := func(decl string) string {
		return `package module

import (
	"` + domains + `/customer"
	"` + domains + `/order"
)

` + decl + "\n"
	}
	runCases(t, []ruleCase{
		{
			name:  "closure",
			edits: edits{file: ports("var reader = func(uc *customer.Usecase) order.CustomerReader { return uc }")},
			want:  []string{file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure},
		},
		{
			name:  "named constructor",
			edits: edits{file: ports("func newReader(uc *customer.Usecase) order.CustomerReader {\n\treturn uc\n}")},
			want:  []string{file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure},
		},
		{
			name:  "fx constructor with an error",
			edits: edits{file: ports("var reader = func(uc *customer.Usecase) (order.CustomerReader, error) { return uc, nil }")},
			want:  []string{file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure},
		},
		{
			name:  "named results",
			edits: edits{file: ports("var reader = func(uc *customer.Usecase) (r order.CustomerReader, err error) { return uc, nil }")},
			want:  []string{file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure},
		},
		{
			name:  "value",
			edits: edits{file: ports("var checker = func(p customer.Policy) order.Checker { return p }")},
			want:  []string{file + " returns customer.Policy as order.Checker: " + whyClosure},
		},
		{
			name: "aliased import",
			edits: edits{file: `package module

import cust "` + domains + `/customer"

var reader = func(uc *cust.Usecase) any { return uc }
`},
			want: []string{file + " returns *cust.Usecase as any: " + whyClosure},
		},
		{
			name:  "next to other parameters",
			edits: edits{file: ports("var reader = func(log *slog.Logger, a, uc *customer.Usecase) (order.CustomerReader, error) { return uc, nil }")},
			want:  []string{file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure},
		},
		{
			name:  "dead code after the return",
			edits: edits{file: ports("func newReader(uc *customer.Usecase) order.CustomerReader {\n\treturn uc\n\tpanic(\"unreachable\")\n}")},
			want:  []string{file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure},
		},
		{
			name: "two closures",
			edits: edits{file: ports("var (\n\treader  = func(uc *customer.Usecase) order.CustomerReader { return uc }\n" +
				"\tchecker = func(p customer.Policy) order.Checker { return p }\n)")},
			want: []string{
				file + " returns *customer.Usecase as order.CustomerReader: " + whyClosure,
				file + " returns customer.Policy as order.Checker: " + whyClosure,
			},
		},
		{
			name: "not an identity function",
			edits: edits{file: ports(`var (
	wrapped = func(uc *customer.Usecase) order.CustomerReader { return &customerReader{uc: uc} }
	failing = func(uc *customer.Usecase) (order.CustomerReader, error) { return uc, errBoom }
	paired  = func(uc *customer.Usecase) (order.CustomerReader, *order.Order) { return uc, nil }
	other   = func(uc *customer.Usecase, r order.CustomerReader) order.CustomerReader { return r }
	local   = func(uc *customer.Usecase) order.CustomerReader { return reader }
	logged  = func(uc *customer.Usecase) order.CustomerReader {
		ready()
		return uc
	}
	own   = func(uc *order.Usecase) order.Placer { return uc }
	plain = func(n int) int { return n }
)`)},
		},
		{
			name: "outside module",
			edits: edits{
				"internal/domain/order/delivery/http/ports.go": "package http\n\nimport (\n\t\"" + domains + "/customer\"\n\t\"" + domains + "/order\"\n)\n\n" +
					"var reader = func(uc *customer.Usecase) order.CustomerReader { return uc }\n",
				"internal/domain/order/module/ports_test.go": ports("var reader = func(uc *customer.Usecase) order.CustomerReader { return uc }"),
				"cmd/api/ports.go": "package main\n\nimport (\n\t\"" + domains + "/customer\"\n\t\"" + domains + "/order\"\n)\n\n" +
					"var reader = func(uc *customer.Usecase) order.CustomerReader { return uc }\n",
			},
			want: []string{"internal/domain/order/delivery/http/ports.go imports internal/domain/customer: " + whyCrossDomain},
		},
	})
}
