package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// Sample is one reading from a laptop.
type Sample struct {
	TS        int64   `json:"ts"`         // unix seconds, set by the agent
	CPU       float64 `json:"cpu"`        // percent
	Mem       float64 `json:"mem"`        // percent
	NetUp     float64 `json:"net_up"`     // bits per second
	NetDown   float64 `json:"net_down"`   // bits per second
	NetErr    float64 `json:"net_err"`    // errors + dropped packets per minute
	LatencyMs float64 `json:"latency_ms"` // how long the previous report took to reach the server
}

type Device struct {
	ID         string          `json:"id"`
	MachineID  string          `json:"machine_id"`
	Name       string          `json:"name"`
	Hostname   string          `json:"hostname"`
	TempleID   string          `json:"temple_id"`
	OS         string          `json:"os"`
	Specs      json.RawMessage `json:"specs"`
	Active     bool            `json:"active"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	LastSeenAt *time.Time      `json:"last_seen_at"`
	Latest     Sample          `json:"-"`
}

var errNotFound = errors.New("not found")

type Store struct{ pool *pgxpool.Pool }

func newStore(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	// Postgres may still be starting when docker compose brings everything up.
	for attempt := 0; ; attempt++ {
		if err = pool.Ping(ctx); err == nil {
			break
		}
		if attempt == 30 {
			return nil, err
		}
		time.Sleep(time.Second)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

const deviceCols = `id::text, machine_id, name, hostname, temple_id, os, specs, active, created_at, updated_at, last_seen_at, latest`

func scanDevice(row pgx.Row) (Device, error) {
	var d Device
	var latest []byte
	err := row.Scan(&d.ID, &d.MachineID, &d.Name, &d.Hostname, &d.TempleID, &d.OS, &d.Specs, &d.Active,
		&d.CreatedAt, &d.UpdatedAt, &d.LastSeenAt, &latest)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errNotFound
	}
	if err == nil && len(latest) > 0 {
		_ = json.Unmarshal(latest, &d.Latest)
	}
	return d, err
}

func (s *Store) ListActive(ctx context.Context) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deviceCols+` FROM devices WHERE active ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices WHERE id::text = $1`, id))
}

// Register is idempotent on machine_id, so restarting an agent refreshes its details
// instead of creating a duplicate.
func (s *Store) Register(ctx context.Context, d Device) (Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `
		INSERT INTO devices (machine_id, name, hostname, temple_id, os, specs)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (machine_id) DO UPDATE SET
			hostname = EXCLUDED.hostname, os = EXCLUDED.os, specs = EXCLUDED.specs,
			temple_id = EXCLUDED.temple_id, active = true, updated_at = now()
		RETURNING `+deviceCols, d.MachineID, d.Name, d.Hostname, d.TempleID, d.OS, d.Specs))
}

func (s *Store) Update(ctx context.Context, id string, name, templeID *string, active *bool) (Device, error) {
	return scanDevice(s.pool.QueryRow(ctx, `
		UPDATE devices SET
			name = COALESCE($2, name), temple_id = COALESCE($3, temple_id),
			active = COALESCE($4, active), updated_at = now()
		WHERE id::text = $1 RETURNING `+deviceCols, id, name, templeID, active))
}

// AddSamples stores a batch and refreshes the device's "latest" reading and last-seen time.
// Re-sending the same reading is harmless (it is ignored), so agents can safely retry.
func (s *Store) AddSamples(ctx context.Context, id string, in []Sample) error {
	n := len(in)
	ts := make([]time.Time, n)
	cpu, mem, up, down, errs, lat := make([]float32, n), make([]float32, n), make([]float32, n), make([]float32, n), make([]float32, n), make([]float32, n)
	var newest Sample
	for i, x := range in {
		ts[i] = time.Unix(x.TS, 0).UTC()
		cpu[i], mem[i], up[i], down[i], errs[i], lat[i] = float32(x.CPU), float32(x.Mem), float32(x.NetUp), float32(x.NetDown), float32(x.NetErr), float32(x.LatencyMs)
		if x.TS >= newest.TS {
			newest = x
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO samples (device_id, ts, cpu, mem, net_up, net_down, net_err, latency_ms)
		SELECT $1::uuid, t, c, m, u, d, e, l
		FROM unnest($2::timestamptz[], $3::float4[], $4::float4[], $5::float4[], $6::float4[], $7::float4[], $8::float4[]) AS x(t, c, m, u, d, e, l)
		ON CONFLICT DO NOTHING`, id, ts, cpu, mem, up, down, errs, lat); err != nil {
		return err
	}
	raw, _ := json.Marshal(newest)
	// last_seen is the server's clock, so a laptop with a wrong clock cannot look "online" when it is not.
	if _, err := tx.Exec(ctx, `
		UPDATE devices SET last_seen_at = now(),
			latest = CASE WHEN COALESCE((latest->>'ts')::bigint, 0) <= $3 THEN $2::jsonb ELSE latest END
		WHERE id = $1::uuid`, id, string(raw), newest.TS); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type SeriesPoint struct {
	T                                int64
	CPU, Mem, Up, Down, Err, Latency *float64
}

// Series averages readings into buckets so a 24 hour chart is about 120 points instead of 5,760.
func (s *Store) Series(ctx context.Context, id string, since time.Time, stepSeconds int) ([]SeriesPoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT (floor(extract(epoch FROM ts) / $3::float8) * $3::float8)::bigint AS t,
		       avg(cpu), avg(mem), avg(net_up), avg(net_down), avg(net_err), avg(latency_ms)
		FROM samples WHERE device_id::text = $1 AND ts >= $2
		GROUP BY 1 ORDER BY 1`, id, since, stepSeconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeriesPoint{}
	for rows.Next() {
		var p SeriesPoint
		if err := rows.Scan(&p.T, &p.CPU, &p.Mem, &p.Up, &p.Down, &p.Err, &p.Latency); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Prune(ctx context.Context, days int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM samples WHERE ts < now() - make_interval(days => $1)`, days)
	return tag.RowsAffected(), err
}
