package drops

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-mint/internal/platform/httpx"
)

const (
	maxUnitsTotal       = 5000
	maxPriceCents       = 1_000_000
	defaultPerCollector = 2
)

// Drop is the public view of a drop.
type Drop struct {
	ID              uuid.UUID `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Series          string    `json:"series"`
	Artist          string    `json:"artist"`
	ReleaseDate     time.Time `json:"releaseDate"`
	PriceCents      int64     `json:"priceCents"`
	UnitsTotal      int       `json:"unitsTotal"`
	UnitsLeft       int       `json:"unitsLeft"`
	MaxPerCollector int       `json:"maxPerCollector"`
}

// Info is what other modules may ask the drops module (Orders never reads this schema).
type Info struct {
	ID         uuid.UUID
	Code       string
	PriceCents int64
}

// Catalog answers the one cross-module read: a drop's existence and price.
type Catalog struct{ Pool *pgxpool.Pool }

// Find looks a drop up by code. It returns nil when there is none.
func (c Catalog) Find(ctx context.Context, code string) (*Info, error) {
	var i Info
	err := c.Pool.QueryRow(ctx, `SELECT id, code, price_cents FROM drops.drops WHERE code = $1`, code).Scan(&i.ID, &i.Code, &i.PriceCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &i, err
}

// API is the drops HTTP surface.
type API struct {
	Pool *pgxpool.Pool
	Auth httpx.Authenticator
	Log  *slog.Logger
}

// Routes registers the drop endpoints; every one needs a signed-in user.
func (a *API) Routes(mux *http.ServeMux) {
	authed := func(h httpx.HandlerFunc) http.HandlerFunc {
		return httpx.Authenticated(a.Log, a.Auth, httpx.Wrap(a.Log, h))
	}
	mux.HandleFunc("GET /api/drops", authed(a.list))
	mux.HandleFunc("GET /api/drops/{code}", authed(a.get))
	mux.HandleFunc("POST /api/drops", httpx.Authenticated(a.Log, a.Auth, httpx.Staff(httpx.Wrap(a.Log, a.create))))
}

const dropSelect = `SELECT id, code, name, series, artist, release_date, price_cents, units_total, units_left, max_per_collector FROM drops.drops`

func scanDrop(row pgx.Row) (Drop, error) {
	var d Drop
	err := row.Scan(&d.ID, &d.Code, &d.Name, &d.Series, &d.Artist, &d.ReleaseDate, &d.PriceCents, &d.UnitsTotal, &d.UnitsLeft, &d.MaxPerCollector)
	return d, err
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	rows, err := a.Pool.Query(r.Context(), dropSelect+` ORDER BY release_date, code`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []Drop{}
	for rows.Next() {
		d, err := scanDrop(rows)
		if err != nil {
			return err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, out)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	d, err := scanDrop(a.Pool.QueryRow(r.Context(), dropSelect+` WHERE code = $1`, r.PathValue("code")))
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Fail(http.StatusNotFound, "Not found.")
	}
	if err != nil {
		return err
	}
	return httpx.JSON(w, http.StatusOK, d)
}

type createRequest struct {
	Code            string     `json:"code"`
	Name            string     `json:"name"`
	Series          string     `json:"series"`
	Artist          string     `json:"artist"`
	ReleaseDate     *time.Time `json:"releaseDate"`
	UnitsTotal      int64      `json:"unitsTotal"`
	PriceCents      int64      `json:"priceCents"`
	MaxPerCollector *int64     `json:"maxPerCollector"`
}

// create opens a drop. Staff only. This is the only place units_left is initialised; after that only the reserve and
// release handlers write it.
func (a *API) create(w http.ResponseWriter, r *http.Request) error {
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > 20 {
		return httpx.Fail(http.StatusBadRequest, "Code is required and at most 20 characters.")
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Series) == "" || strings.TrimSpace(req.Artist) == "" {
		return httpx.Fail(http.StatusBadRequest, "Name, series and artist are required.")
	}
	if req.UnitsTotal < 1 || req.UnitsTotal > maxUnitsTotal {
		return httpx.Fail(http.StatusBadRequest, "Units on sale must be between 1 and %d units.", maxUnitsTotal)
	}
	if req.PriceCents < 1 || req.PriceCents > maxPriceCents {
		return httpx.Fail(http.StatusBadRequest, "Price must be between 1 and %d cents per unit.", maxPriceCents)
	}
	limit := int64(defaultPerCollector)
	if req.MaxPerCollector != nil {
		limit = *req.MaxPerCollector
	}
	if limit < 1 || limit > req.UnitsTotal {
		return httpx.Fail(http.StatusBadRequest, "Per-collector cap must be at least 1 and no more than the units on sale.")
	}
	release := time.Now().AddDate(0, 0, 14)
	if req.ReleaseDate != nil {
		release = *req.ReleaseDate
	}
	id, _ := uuid.NewV7()
	_, err := a.Pool.Exec(r.Context(), `
		INSERT INTO drops.drops (id, code, name, series, artist, release_date, price_cents, units_total, units_left, max_per_collector, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,now())`,
		id, code, strings.TrimSpace(req.Name), strings.TrimSpace(req.Series), strings.TrimSpace(req.Artist), release, req.PriceCents, req.UnitsTotal, limit)
	if err != nil {
		var coder interface{ SQLState() string }
		if errors.As(err, &coder) && coder.SQLState() == "23505" {
			return httpx.Fail(http.StatusConflict, "Drop '%s' already exists.", code)
		}
		return err
	}
	d, err := scanDrop(a.Pool.QueryRow(r.Context(), dropSelect+` WHERE id = $1`, id))
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/api/drops/"+code)
	return httpx.JSON(w, http.StatusCreated, d)
}
