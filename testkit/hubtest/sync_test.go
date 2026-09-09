package hubtest_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"git.stit.tech/stit-core/golibinfra/hubkit/client"
	hubsync "git.stit.tech/stit-core/golibinfra/hubkit/sync"
	"git.stit.tech/stit-core/golibinfra/logkit"
	"git.stit.tech/stit-core/golibinfra/testkit/hubtest"
)

const syncResource = client.ResourceCompanies

// syncRig — фейки синка и движок поверх них: фид из двух сущностей, которые
// один тик догоняет целиком (Limit больше фида).
type syncRig struct {
	feed   *hubtest.Feed
	proj   *hubtest.Projection
	cur    *hubtest.Cursors
	engine *hubsync.Engine
}

func newSyncRig(t *testing.T) syncRig {
	t.Helper()
	rig := syncRig{feed: hubtest.NewFeed(), proj: hubtest.NewProjection(), cur: hubtest.NewCursors()}
	rig.feed.Add(t, syncResource, 1, map[string]any{"id": "c1", "updated_seq": 1})
	rig.feed.Add(t, syncResource, 2, map[string]any{"id": "c2", "updated_seq": 2})

	engine, err := hubsync.NewEngine(syncResource, rig.feed, rig.proj, rig.cur,
		hubsync.Config{Interval: time.Minute, Limit: 10}, logkit.Nop())
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	rig.engine = engine
	return rig
}

func (r syncRig) applied(t *testing.T) []string {
	t.Helper()
	items := r.proj.Items(syncResource)
	ids := make([]string, 0, len(items))
	for _, raw := range items {
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatalf("unmarshal item: %v", err)
		}
		ids = append(ids, item.ID)
	}
	return ids
}

func TestSyncFakes_FailInjectors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		arm  func(syncRig)
		drop func(syncRig)
		// где именно встал тик под отказом
		wantFetches int
		wantSaves   int
		wantApplied []string
		// что применено после снятия отказа и повторного тика
		wantAppliedAfter []string
	}{
		{
			name:             "cursor load",
			arm:              func(r syncRig) { r.cur.FailLoad(boom) },
			drop:             func(r syncRig) { r.cur.FailLoad(nil) },
			wantFetches:      0,
			wantSaves:        0,
			wantApplied:      nil,
			wantAppliedAfter: []string{"c1", "c2"},
		},
		{
			name:             "feed fetch",
			arm:              func(r syncRig) { r.feed.FailWith(syncResource, boom) },
			drop:             func(r syncRig) { r.feed.FailWith(syncResource, nil) },
			wantFetches:      1,
			wantSaves:        0,
			wantApplied:      nil,
			wantAppliedAfter: []string{"c1", "c2"},
		},
		{
			name:             "projection upsert",
			arm:              func(r syncRig) { r.proj.FailWith(boom) },
			drop:             func(r syncRig) { r.proj.FailWith(nil) },
			wantFetches:      1,
			wantSaves:        0,
			wantApplied:      nil,
			wantAppliedAfter: []string{"c1", "c2"},
		},
		{
			name:        "cursor save",
			arm:         func(r syncRig) { r.cur.FailSave(boom) },
			drop:        func(r syncRig) { r.cur.FailSave(nil) },
			wantFetches: 1,
			wantSaves:   1,
			// батч применён до отказа: FailSave ловит лишь продвижение курсора
			wantApplied: []string{"c1", "c2"},
			// поэтому повторный тик применяет тот же батч ещё раз
			wantAppliedAfter: []string{"c1", "c2", "c1", "c2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newSyncRig(t)
			tc.arm(rig)

			if err := rig.engine.Run(t.Context()); !errors.Is(err, boom) {
				t.Fatalf("Run under the injected failure = %v, want %v", err, boom)
			}
			if got := rig.feed.Calls(); got != tc.wantFetches {
				t.Fatalf("Calls = %d, want %d", got, tc.wantFetches)
			}
			if got := rig.cur.Saves(); got != tc.wantSaves {
				t.Fatalf("Saves = %d, want %d", got, tc.wantSaves)
			}
			if got := rig.applied(t); !slices.Equal(got, tc.wantApplied) {
				t.Fatalf("applied %v, want %v", got, tc.wantApplied)
			}
			if got := rig.cur.Cursor(syncResource); got != 0 {
				t.Fatalf("cursor advanced on a failed tick: %d", got)
			}

			tc.drop(rig)
			if err := rig.engine.Run(t.Context()); err != nil {
				t.Fatalf("Run after the failure was cleared: %v", err)
			}
			if got := rig.cur.Cursor(syncResource); got != 2 {
				t.Fatalf("cursor = %d, want 2", got)
			}
			if got := rig.applied(t); !slices.Equal(got, tc.wantAppliedAfter) {
				t.Fatalf("applied after the failure was cleared %v, want %v", got, tc.wantAppliedAfter)
			}
		})
	}
}

func TestCursors_Set(t *testing.T) {
	rig := newSyncRig(t)
	rig.cur.Set(syncResource, 1)
	if got := rig.cur.Cursor(syncResource); got != 1 {
		t.Fatalf("Cursor = %d, want 1", got)
	}

	if err := rig.engine.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := rig.applied(t); !slices.Equal(got, []string{"c2"}) {
		t.Fatalf("applied %v, want [c2]: a tick starts past the cursor set by Set", got)
	}
	if got := rig.cur.Cursor(syncResource); got != 2 {
		t.Fatalf("Cursor = %d, want 2", got)
	}
}
