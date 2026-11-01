package main

import (
	"database/sql"
	"errors"
	"time"
)

// Action statuses. A proposal moves proposed -> approved -> executed | failed, or ends as
// rejected / expired. "approved" is held only while the action runs.
const (
	ActionProposed  = "proposed"
	ActionApproved  = "approved"
	ActionRejected  = "rejected"
	ActionExecuted  = "executed"
	ActionFailed    = "failed"
	ActionExpired   = "expired"
	KindRestart     = "restart"
	KindRollback    = "rollback"
	KindOpenPR      = "open-pr"
	ActorSystem     = "system"
	auditProposed   = "proposed"
	auditApproved   = "approved"
	auditRejected   = "rejected"
	auditExecuted   = "executed"
	auditDryRun     = "dry-run"
	auditFailed     = "failed"
	auditBlocked    = "blocked"
	auditExpired    = "expired"
	auditVerified   = "verified"
	auditVerifyFail = "verify-failed"
)

// Action is something the platform may do to a workload, always after a human approves it.
type Action struct {
	ID         int64
	IncidentID int64
	Kind       string
	Namespace  string
	Target     string // Deployment name
	Params     string // JSON, kind specific
	Rule       string // which rule proposed it (or "manual-slack-restart")
	Reason     string
	Status     string
	DecidedBy  string
	Result     string
	CreatedAt  time.Time
	DecidedAt  time.Time
	ExecutedAt time.Time
}

// AuditEntry is one immutable line of the audit log.
type AuditEntry struct {
	ID         int64
	At         time.Time
	Actor      string
	Event      string
	IncidentID int64
	ActionID   int64
	Detail     string
}

const actionSchema = `
CREATE TABLE IF NOT EXISTS actions (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	incident_id INTEGER NOT NULL,
	kind        TEXT NOT NULL,
	namespace   TEXT NOT NULL,
	target      TEXT NOT NULL,
	params      TEXT NOT NULL DEFAULT '{}',
	rule        TEXT NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'proposed',
	decided_by  TEXT NOT NULL DEFAULT '',
	result      TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL,
	decided_at  INTEGER NOT NULL DEFAULT 0,
	executed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_actions_incident ON actions(incident_id);
CREATE TABLE IF NOT EXISTS audit (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	ts          INTEGER NOT NULL,
	actor       TEXT NOT NULL,
	event       TEXT NOT NULL,
	incident_id INTEGER NOT NULL DEFAULT 0,
	action_id   INTEGER NOT NULL DEFAULT 0,
	detail      TEXT NOT NULL DEFAULT ''
);
-- The audit log is append-only: the database itself refuses edits and deletes.
CREATE TRIGGER IF NOT EXISTS audit_no_update BEFORE UPDATE ON audit
BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END;
CREATE TRIGGER IF NOT EXISTS audit_no_delete BEFORE DELETE ON audit
BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END;
`

const actionCols = `id, incident_id, kind, namespace, target, params, rule, reason, status, decided_by, result, created_at, decided_at, executed_at`

func scanAction(row interface{ Scan(...any) error }) (*Action, error) {
	var a Action
	var created, decided, executed int64
	if err := row.Scan(&a.ID, &a.IncidentID, &a.Kind, &a.Namespace, &a.Target, &a.Params, &a.Rule, &a.Reason,
		&a.Status, &a.DecidedBy, &a.Result, &created, &decided, &executed); err != nil {
		return nil, err
	}
	a.CreatedAt = time.Unix(created, 0).UTC()
	if decided > 0 {
		a.DecidedAt = time.Unix(decided, 0).UTC()
	}
	if executed > 0 {
		a.ExecutedAt = time.Unix(executed, 0).UTC()
	}
	return &a, nil
}

func (s *Store) CreateAction(a Action) (*Action, error) {
	res, err := s.db.Exec(`INSERT INTO actions (incident_id, kind, namespace, target, params, rule, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.IncidentID, a.Kind, a.Namespace, a.Target, orDefault(a.Params, "{}"), a.Rule, a.Reason, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetAction(id)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (s *Store) GetAction(id int64) (*Action, error) {
	return scanAction(s.db.QueryRow(`SELECT `+actionCols+` FROM actions WHERE id = ?`, id))
}

func (s *Store) ActionsForIncident(incidentID int64) ([]Action, error) {
	rows, err := s.db.Query(`SELECT `+actionCols+` FROM actions WHERE incident_id = ? ORDER BY id`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// Claim atomically moves a proposed action to approved (or rejected / expired). It returns false if
// someone else already decided it, so a double click or two approvers can never run an action twice.
func (s *Store) Claim(id int64, to, actor string) (bool, error) {
	res, err := s.db.Exec(`UPDATE actions SET status = ?, decided_by = ?, decided_at = ? WHERE id = ? AND status = 'proposed'`,
		to, actor, time.Now().Unix(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) FinishAction(id int64, status, result string) error {
	_, err := s.db.Exec(`UPDATE actions SET status = ?, result = ?, executed_at = ? WHERE id = ?`,
		status, result, time.Now().Unix(), id)
	return err
}

// LastExecuted returns when an action last ran (or was dry-run) against the same workload, for the cooldown.
func (s *Store) LastExecuted(namespace, target string) (time.Time, error) {
	var ts sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(executed_at) FROM actions WHERE namespace = ? AND target = ? AND status = 'executed' AND kind != 'open-pr'`,
		namespace, target).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) || !ts.Valid || ts.Int64 == 0 {
		return time.Time{}, err
	}
	return time.Unix(ts.Int64, 0).UTC(), nil
}

// Audit appends an entry. There is deliberately no update or delete path.
func (s *Store) Audit(actor, event string, incidentID, actionID int64, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit (ts, actor, event, incident_id, action_id, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, event, incidentID, actionID, detail)
	return err
}

func (s *Store) ListAudit(limit int, incidentID int64) ([]AuditEntry, error) {
	q, args := `SELECT id, ts, actor, event, incident_id, action_id, detail FROM audit`, []any{}
	if incidentID > 0 {
		q += ` WHERE incident_id = ?`
		args = append(args, incidentID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Event, &e.IncidentID, &e.ActionID, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(ts, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PendingActions counts proposals still waiting for a decision, per incident.
func (s *Store) PendingActions() (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT incident_id, COUNT(*) FROM actions WHERE status = 'proposed' GROUP BY incident_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
