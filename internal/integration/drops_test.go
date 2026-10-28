package integration

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-mint/internal/drops"
	"github.com/joshbarros/golang-mint/internal/platform/messaging"
)

// rawDrop inserts a drop directly (below the API) and returns its id.
func (e *env) rawDrop(units, perCollector int) uuid.UUID {
	e.t.Helper()
	id := uuid.New()
	_, err := e.pool.Exec(context.Background(), `INSERT INTO drops.drops (id, code, name, series, artist, release_date, price_cents, units_total, units_left, max_per_collector, created_at)
		VALUES ($1,$2,'n','s','a',now(),5000,$3,$3,$4,now())`, id, code(), units, perCollector)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func reserve(e *env, drop, owner uuid.UUID, units int) drops.ReserveOutcome {
	out, err := e.app.Drops.Reserve(context.Background(), uuid.New(), messaging.ReserveUnits{OrderID: uuid.New(), OwnerID: owner, DropID: drop, Units: units, Reference: "R"})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestConcurrentReservesNeverOversellAndAlwaysSumToTheStock(t *testing.T) {
	e := newEnv(t)
	drop := e.rawDrop(30, 3)
	var wg sync.WaitGroup
	var mu sync.Mutex
	held := 0
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reserve(e, drop, uuid.New(), 1) == drops.Held {
				mu.Lock()
				held++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var left int
	_ = e.pool.QueryRow(context.Background(), `SELECT units_left FROM drops.drops WHERE id = $1`, drop).Scan(&left)
	if held != 30 || left != 0 {
		t.Fatalf("60 collectors, 30 units: exactly 30 held and none left, got %d held and %d left", held, left)
	}
	if e.count(`SELECT count(*) FROM drops.unit_holds WHERE drop_id = $1`, drop) != 30 {
		t.Fatal("one hold per success")
	}
}

func TestTheSameCollectorRacingThemselvesCannotPassTheCap(t *testing.T) {
	e := newEnv(t)
	drop := e.rawDrop(50, 2)
	owner := uuid.New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	outcomes := map[drops.ReserveOutcome]int{}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := reserve(e, drop, owner, 1)
			mu.Lock()
			outcomes[o]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if outcomes[drops.Held] != 2 || outcomes[drops.CapReached] != 10 {
		t.Fatalf("a cap of 2 lets exactly 2 through: %v", outcomes)
	}
	var left, holding int
	_ = e.pool.QueryRow(context.Background(), `SELECT units_left FROM drops.drops WHERE id = $1`, drop).Scan(&left)
	_ = e.pool.QueryRow(context.Background(), `SELECT units FROM drops.collectors WHERE drop_id = $1 AND owner_id = $2`, drop, owner).Scan(&holding)
	if left != 48 || holding != 2 {
		t.Fatalf("refused tries leave no trace: %d left, collector holds %d", left, holding)
	}
}

func TestACapRefusalDoesNotConsumeStockAndAStockRefusalDoesNotConsumeAllowance(t *testing.T) {
	e := newEnv(t)
	drop := e.rawDrop(3, 2)
	owner := uuid.New()
	if reserve(e, drop, owner, 3) != drops.CapReached {
		t.Fatal("asking for more than the cap in one go is a cap refusal")
	}
	other := uuid.New()
	if reserve(e, drop, other, 2) != drops.Held {
		t.Fatal("2 of 3 held")
	}
	if reserve(e, drop, owner, 2) != drops.Short {
		t.Fatal("1 left, 2 wanted, within the cap: short")
	}
	if reserve(e, drop, owner, 1) != drops.Held {
		t.Fatal("the allowance was not spent by the short try")
	}
	var left int
	_ = e.pool.QueryRow(context.Background(), `SELECT units_left FROM drops.drops WHERE id = $1`, drop).Scan(&left)
	if left != 0 {
		t.Fatalf("all three are held: %d", left)
	}
}

func TestReleaseGivesBackStockAndAllowanceExactlyOnce(t *testing.T) {
	e := newEnv(t)
	drop := e.rawDrop(5, 2)
	owner, order := uuid.New(), uuid.New()
	if out, _ := e.app.Drops.Reserve(context.Background(), uuid.New(), messaging.ReserveUnits{OrderID: order, OwnerID: owner, DropID: drop, Units: 2, Reference: "R"}); out != drops.Held {
		t.Fatal(out)
	}
	for i := 0; i < 3; i++ {
		_, err := e.app.Drops.Release(context.Background(), uuid.New(), messaging.ReleaseUnits{OrderID: order, Reason: "refund"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var left, holding int
	_ = e.pool.QueryRow(context.Background(), `SELECT units_left FROM drops.drops WHERE id = $1`, drop).Scan(&left)
	_ = e.pool.QueryRow(context.Background(), `SELECT units FROM drops.collectors WHERE drop_id = $1 AND owner_id = $2`, drop, owner).Scan(&holding)
	if left != 5 || holding != 0 {
		t.Fatalf("given back once, not three times: %d left, collector holds %d", left, holding)
	}
	if e.outbox("drops", messaging.ReleasedKey) != 1 {
		t.Fatal("one released event")
	}
}

func TestOpeningADropValidatesItsCap(t *testing.T) {
	e := newEnv(t)
	adm := e.admin()
	good := map[string]any{"code": code(), "name": "n", "series": "s", "artist": "a", "priceCents": 100, "unitsTotal": 10}
	if r := e.post("/api/drops", good, adm.Token); r.Status != 201 || r.Body["maxPerCollector"] != float64(2) {
		t.Fatalf("the cap defaults to 2: %d %s", r.Status, r.Raw)
	}
	bad := map[string]any{"code": code(), "name": "n", "series": "s", "artist": "a", "priceCents": 100, "unitsTotal": 10, "maxPerCollector": 11}
	if r := e.post("/api/drops", bad, adm.Token); r.Status != 400 {
		t.Fatalf("a cap above the units on sale: %d", r.Status)
	}
	bad["maxPerCollector"] = 0
	if r := e.post("/api/drops", bad, adm.Token); r.Status != 400 {
		t.Fatalf("a cap of zero: %d", r.Status)
	}
	if r := e.post("/api/drops", good, adm.Token); r.Status != 409 {
		t.Fatalf("the same code twice: %d", r.Status)
	}
	if r := e.post("/api/drops", good, e.register().Token); r.Status != 403 {
		t.Fatalf("a customer cannot open a drop: %d", r.Status)
	}
}
