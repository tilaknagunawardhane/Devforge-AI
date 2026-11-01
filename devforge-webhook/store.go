package main

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver: no CGO, so the image stays static and tiny
)

const (
	StatusOpen         = "open"
	StatusAcknowledged = "acknowledged"
	StatusResolved     = "resolved"
)

// Incident is one group of related alerts, analysed once.
type Incident struct {
	ID         int64
	Key        string // Alertmanager group key (or a derived one) used for deduplication
	Subject    string
	Namespace  string
	Alertnames string // comma separated
	Pods       string // comma separated
	Status     string
	AlertCount int
	RCA        string
	AckedBy    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (i Incident) PodList() []string {
	if i.Pods == "" {
		return nil
	}
	return strings.Split(i.Pods, ",")
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS incidents (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	key         TEXT NOT NULL,
	subject     TEXT NOT NULL,
	namespace   TEXT NOT NULL,
	alertnames  TEXT NOT NULL DEFAULT '',
	pods        TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'open',
	alert_count INTEGER NOT NULL DEFAULT 1,
	rca         TEXT NOT NULL DEFAULT '',
	acked_by    TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_incidents_key ON incidents(key, updated_at);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer; one connection avoids "database is locked"
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if _, err := db.Exec(actionSchema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const cols = `id, key, subject, namespace, alertnames, pods, status, alert_count, rca, acked_by, created_at, updated_at`

func scan(row interface{ Scan(...any) error }) (*Incident, error) {
	var i Incident
	var created, updated int64
	if err := row.Scan(&i.ID, &i.Key, &i.Subject, &i.Namespace, &i.Alertnames, &i.Pods, &i.Status,
		&i.AlertCount, &i.RCA, &i.AckedBy, &created, &updated); err != nil {
		return nil, err
	}
	i.CreatedAt, i.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return &i, nil
}

func (s *Store) Create(i Incident) (*Incident, error) {
	now := time.Now().Unix()
	res, err := s.db.Exec(`INSERT INTO incidents (key, subject, namespace, alertnames, pods, status, alert_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
		i.Key, i.Subject, i.Namespace, i.Alertnames, i.Pods, max(i.AlertCount, 1), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

// FindRecent returns the newest unresolved incident for key that was updated within window.
func (s *Store) FindRecent(key string, window time.Duration) (*Incident, error) {
	cutoff := time.Now().Add(-window).Unix()
	i, err := scan(s.db.QueryRow(`SELECT `+cols+` FROM incidents
		WHERE key = ? AND status != 'resolved' AND updated_at >= ? ORDER BY id DESC LIMIT 1`, key, cutoff))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return i, err
}

// AddAlerts folds a repeated notification into an existing incident instead of opening a new one.
// Alertmanager re-sends a still-firing group every group_interval, so the alert count only grows
// by pods the incident has not seen before; a repeat of the same alerts changes nothing but the
// last-updated time.
func (s *Store) AddAlerts(id int64, pods []string) error {
	cur, err := s.Get(id)
	if err != nil {
		return err
	}
	merged := mergeUnique(cur.PodList(), pods)
	added := len(merged) - len(cur.PodList())
	_, err = s.db.Exec(`UPDATE incidents SET alert_count = alert_count + ?, pods = ?, updated_at = ? WHERE id = ?`,
		added, strings.Join(merged, ","), time.Now().Unix(), id)
	return err
}

func (s *Store) SetRCA(id int64, rca string) error {
	_, err := s.db.Exec(`UPDATE incidents SET rca = ?, updated_at = ? WHERE id = ?`, rca, time.Now().Unix(), id)
	return err
}

func (s *Store) SetStatus(id int64, status, by string) error {
	_, err := s.db.Exec(`UPDATE incidents SET status = ?, acked_by = CASE WHEN ? != '' THEN ? ELSE acked_by END, updated_at = ? WHERE id = ?`,
		status, by, by, time.Now().Unix(), id)
	return err
}

// ResolveByKey marks every open incident of a group resolved (Alertmanager "resolved" notification).
func (s *Store) ResolveByKey(key string) error {
	_, err := s.db.Exec(`UPDATE incidents SET status = 'resolved', updated_at = ? WHERE key = ? AND status != 'resolved'`,
		time.Now().Unix(), key)
	return err
}

func (s *Store) Get(id int64) (*Incident, error) {
	return scan(s.db.QueryRow(`SELECT `+cols+` FROM incidents WHERE id = ?`, id))
}

func (s *Store) List(limit int) ([]Incident, error) {
	rows, err := s.db.Query(`SELECT `+cols+` FROM incidents ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

func mergeUnique(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range append(append([]string{}, a...), b...) {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
