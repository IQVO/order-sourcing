package repocontract

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/order-sourcing/internal/application/ports"
)

func runUnitOfWork(t *testing.T, f Factory) {
	t.Run("commit keeps aggregate, outbox and inbox together", func(t *testing.T) { uowCommit(t, f(t)) })
	t.Run("an error rolls everything back", func(t *testing.T) { uowRollback(t, f(t)) })
	t.Run("a panic rolls everything back", func(t *testing.T) { uowPanic(t, f(t)) })
	t.Run("nested units join the outer one", func(t *testing.T) { uowNested(t, f(t)) })
}

func uowCommit(t *testing.T, fx Fixture) {
	a := queued(t, "asg-1", "ord-1", 1, hours(2))
	err := fx.UoW.Do(ctx(), func(c context.Context) error {
		if err := fx.Assignments.Save(c, a); err != nil {
			return err
		}
		if err := fx.Outbox.Append(c, eventsOf(t, a)...); err != nil {
			return err
		}
		_, err := fx.Inbox.Claim(c, "evt-1", "com.retailnetwork.x")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.Assignments.Get(ctx(), "asg-1"); err != nil {
		t.Fatalf("committed aggregate: %v", err)
	}
	if n := fx.OutboxCount(t); n != 1 {
		t.Fatalf("outbox = %d, want 1", n)
	}
	if first, _ := fx.Inbox.Claim(ctx(), "evt-1", "x"); first {
		t.Fatal("the committed claim must stay")
	}
}

func uowRollback(t *testing.T, fx Fixture) {
	boom := errors.New("boom")
	a := queued(t, "asg-1", "ord-1", 1, hours(2))
	err := fx.UoW.Do(ctx(), func(c context.Context) error {
		if err := fx.Assignments.Save(c, a); err != nil {
			return err
		}
		if err := fx.Outbox.Append(c, eventsOf(t, a)...); err != nil {
			return err
		}
		if _, err := fx.Inbox.Claim(c, "evt-1", "x"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do returned %v, want the function's error unchanged", err)
	}
	wantEmpty(t, fx)
}

func uowPanic(t *testing.T, fx Fixture) {
	a := queued(t, "asg-1", "ord-1", 1, hours(2))
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic must propagate")
			}
		}()
		_ = fx.UoW.Do(ctx(), func(c context.Context) error {
			if err := fx.Assignments.Save(c, a); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	wantEmpty(t, fx)
}

func uowNested(t *testing.T, fx Fixture) {
	a := queued(t, "asg-1", "ord-1", 1, hours(2))
	boom := errors.New("boom")
	err := fx.UoW.Do(ctx(), func(c context.Context) error {
		if err := fx.UoW.Do(c, func(c context.Context) error { return fx.Assignments.Save(c, a) }); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	wantEmpty(t, fx)
}

func wantEmpty(t *testing.T, fx Fixture) {
	t.Helper()
	if _, err := fx.Assignments.Get(ctx(), "asg-1"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("rolled back aggregate: %v", err)
	}
	if n := fx.OutboxCount(t); n != 0 {
		t.Fatalf("outbox = %d after rollback", n)
	}
	if first, err := fx.Inbox.Claim(ctx(), "evt-1", "x"); err != nil || !first {
		t.Fatalf("the rolled back claim must be free: %v %v", first, err)
	}
}
