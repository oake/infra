package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	_ "modernc.org/sqlite"
)

// Domain records are JSON in SQLite; SQL keeps identity and transactions durable.
var collections = []string{"hosts", "repositories", "commits", "artifacts", "observations", "jobs", "pull_requests", "events"}

type Store struct {
	DB     *sql.DB
	writer sync.Mutex
}

func Open(ctx context.Context, path string) (*Store, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(absolute), 0700); e != nil {
		return nil, e
	}
	// Configure every pooled connection, including ones opened after startup.
	u := url.URL{Scheme: "file", Path: absolute}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	p, e := sql.Open("sqlite", u.String())
	if e != nil {
		return nil, e
	}
	p.SetMaxOpenConns(4)
	p.SetMaxIdleConns(4)
	s := &Store{DB: p}
	for _, table := range collections {
		_, e = p.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (id text PRIMARY KEY, body text NOT NULL CHECK (json_valid(body)))`)
		if e != nil {
			p.Close()
			return nil, e
		}
	}
	for _, q := range []string{`CREATE INDEX IF NOT EXISTS observations_host ON observations ((body->>'host'))`, `CREATE INDEX IF NOT EXISTS jobs_repository ON jobs ((body->>'repository'))`, `CREATE INDEX IF NOT EXISTS commits_repository ON commits ((body->>'repository'))`} {
		if _, e = p.ExecContext(ctx, q); e != nil {
			p.Close()
			return nil, e
		}
	}
	return s, nil
}
func load(ctx context.Context, tx *sql.Tx) (*State, error) {
	s := NewState()
	fields := reflect.ValueOf(s).Elem()
	for i, table := range collections {
		rows, e := tx.QueryContext(ctx, "SELECT id,body FROM "+table)
		if e != nil {
			return nil, e
		}
		field := fields.Field(i)
		for rows.Next() {
			var id string
			var b []byte
			if e = rows.Scan(&id, &b); e != nil {
				rows.Close()
				return nil, e
			}
			value := reflect.New(field.Type().Elem())
			if e = json.Unmarshal(b, value.Interface()); e != nil {
				rows.Close()
				return nil, e
			}
			field.SetMapIndex(reflect.ValueOf(id), value.Elem())
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (s *Store) Read(ctx context.Context) (*State, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	state, e := load(ctx, tx)
	if e != nil {
		return nil, e
	}
	return state, tx.Commit()
}
func (s *Store) Update(ctx context.Context, fn func(*State) error) error {
	s.writer.Lock()
	defer s.writer.Unlock()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	state, e := load(ctx, tx)
	if e != nil {
		return e
	}
	// Remember canonical bytes so heartbeats don't rewrite the entire database.
	before := map[string]map[string]string{}
	fields := reflect.ValueOf(state).Elem()
	for i, table := range collections {
		before[table] = map[string]string{}
		iter := fields.Field(i).MapRange()
		for iter.Next() {
			b, _ := json.Marshal(iter.Value().Interface())
			before[table][iter.Key().String()] = string(b)
		}
	}
	if e = fn(state); e != nil {
		return e
	}
	// Only build/commit/PR changes can alter PR summaries. Heartbeats and
	// deployment results must not recalculate every PR.
	changed := false
	for _, i := range []int{2, 3, 6} {
		field := fields.Field(i)
		old := before[collections[i]]
		if len(old) != field.Len() {
			changed = true
			break
		}
		iter := field.MapRange()
		for iter.Next() {
			b, _ := json.Marshal(iter.Value().Interface())
			if old[iter.Key().String()] != string(b) {
				changed = true
				break
			}
		}
		if changed {
			break
		}
	}
	if changed {
		state.RefreshPullRequests()
	}
	for i, table := range collections {
		iter := fields.Field(i).MapRange()
		for iter.Next() {
			id := iter.Key().String()
			b, e := json.Marshal(iter.Value().Interface())
			if e != nil {
				return e
			}
			if before[table][id] == string(b) {
				continue
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO "+table+" (id,body) VALUES ($1,$2) ON CONFLICT (id) DO UPDATE SET body=excluded.body", id, string(b)); e != nil {
				return fmt.Errorf("save %s: %w", table, e)
			}
		}
	}
	for i, table := range collections {
		for id := range before[table] {
			if !fields.Field(i).MapIndex(reflect.ValueOf(id)).IsValid() {
				if _, e = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id=$1", id); e != nil {
					return e
				}
			}
		}
	}
	return tx.Commit()
}

// JSON arrays let scoped queries bind variable-length sets without SQL interpolation.
func jsonStrings(values []string) string {
	b, _ := json.Marshal(values)
	return string(b)
}
