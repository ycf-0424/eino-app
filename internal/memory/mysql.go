package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"my-eino-app/internal/config"
	"strconv"
	"strings"
	"time"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

type Repository struct {
	DB                *sql.DB
	Config            config.Memory
	Generation, Model string
}

func (r *Repository) scope(kind string) Scope {
	id := r.Config.ProjectID
	if kind == "user" {
		id = r.Config.OwnerID
	}
	return Scope{kind, id, r.Config.OwnerID}
}
func (r *Repository) lock(ctx context.Context, tx *sql.Tx) error {
	if _, e := tx.ExecContext(ctx, "INSERT IGNORE INTO memory_locks(owner_id) VALUES(?)", r.Config.OwnerID); e != nil {
		return e
	}
	var id string
	return tx.QueryRowContext(ctx, "SELECT owner_id FROM memory_locks WHERE owner_id=? FOR UPDATE", r.Config.OwnerID).Scan(&id)
}
func (r *Repository) CheckBinding(ctx context.Context, id string) error {
	var owner, project string
	e := r.DB.QueryRowContext(ctx, "SELECT owner_id,project_id FROM memory_bindings WHERE session_id=?", id).Scan(&owner, &project)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if owner != r.Config.OwnerID || project != r.Config.ProjectID {
		return errors.New("memory session scope mismatch")
	}
	return nil
}

// Capture runs inside the session transaction, including streaming progress.
// Only a newly tagged user turn is considered; historical imports are excluded.
func (r *Repository) Capture(ctx context.Context, tx *sql.Tx, id string, msgs []*schema.Message) error {
	if !r.Config.AutoExtract || len(msgs) == 0 {
		return nil
	}
	last := msgs[len(msgs)-1]
	if last == nil || last.Role != schema.Assistant {
		return nil
	}
	user := -1
	for i := len(msgs) - 2; i >= 0; i-- {
		if msgs[i] != nil && msgs[i].Role == schema.User {
			user = i
			break
		}
	}
	if user < 0 {
		return nil
	}
	turnID, _ := msgs[user].Extra["memory_turn"].(string)
	if turnID == "" {
		return nil
	}
	if _, e := tx.ExecContext(ctx, "INSERT IGNORE INTO memory_bindings(session_id,owner_id,project_id) VALUES(?,?,?)", id, r.Config.OwnerID, r.Config.ProjectID); e != nil {
		return e
	}
	var owner, project string
	if e := tx.QueryRowContext(ctx, "SELECT owner_id,project_id FROM memory_bindings WHERE session_id=? FOR UPDATE", id).Scan(&owner, &project); e != nil {
		return e
	}
	if owner != r.Config.OwnerID || project != r.Config.ProjectID {
		return errors.New("memory session scope mismatch")
	}
	status, _ := last.Extra["storage_status"].(string)
	if status == "" {
		return nil
	}
	in := ExtractInput{Message: Message{turnID, "user", msgs[user].Content}, CurrentTime: time.Now().Format(time.RFC3339)}
	blocked := Sensitive(in.Message.Text) || Suppressed(in.Message.Text)
	start := user - r.Config.ContextMessages
	if start < 0 {
		start = 0
	}
	for i := start; i < user; i++ {
		m := msgs[i]
		if m == nil || m.Role != schema.User && m.Role != schema.Assistant || Sensitive(m.Content) || Suppressed(m.Content) {
			continue
		}
		text := m.Content
		if len([]rune(text)) > 1000 {
			text = string([]rune(text)[:1000])
		}
		in.Context = append(in.Context, Message{Role: string(m.Role), Text: text})
	}
	if blocked {
		in.Message.Text = ""
		in.Context = nil
	}
	if len([]rune(in.Message.Text)) > 8000 {
		blocked = true
		in.Message.Text = ""
		in.Context = nil
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return e
	}
	// Only first capture stores evidence. Subsequent calls cannot overwrite it.
	_, e = tx.ExecContext(ctx, `INSERT IGNORE INTO memory_turns(turn_id,session_id,owner_id,project_id,source_message_id,input,state,suppressed) VALUES(?,?,?,?,?,?,?,?)`, turnID, id, owner, project, turnID, raw, status, blocked)
	if e != nil {
		return e
	}
	// A suppression request must also invalidate older extract jobs that may
	// already be queued or running. The wildcard is bounded by this turn's
	// sequence, so it protects prior work without becoming a permanent ban on
	// future memories. Sensitive-only turns do not create this control row.
	if Suppressed(msgs[user].Content) {
		var seq int64
		if e = tx.QueryRowContext(ctx, "SELECT seq FROM memory_turns WHERE turn_id=? FOR UPDATE", turnID).Scan(&seq); e != nil {
			return e
		}
		for _, scope := range []Scope{r.scope("user"), r.scope("project")} {
			if _, e = tx.ExecContext(ctx, `INSERT INTO memory_controls(owner_id,scope_kind,scope_id,fact_key,through_seq) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE through_seq=GREATEST(through_seq,VALUES(through_seq))`, scope.Owner, scope.Kind, scope.ID, "*", seq); e != nil {
				return e
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE memory_turns SET state=?,updated_at=NOW(6) WHERE turn_id=? AND state NOT IN ('completed','processed','deleted')`, status, turnID); e != nil {
		return e
	}
	if status == "completed" {
		return r.enqueue(ctx, tx, "extract", turnID, 0, "")
	}
	return nil
}

func (r *Repository) enqueue(ctx context.Context, tx *sql.Tx, kind, target string, version int64, generation string) error {
	dedupe := kind + ":" + target + ":" + itoa(version) + ":" + generation
	_, e := tx.ExecContext(ctx, `INSERT IGNORE INTO memory_jobs(id,owner_id,project_id,kind,target,version,generation,dedupe_key) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), r.Config.OwnerID, r.Config.ProjectID, kind, target, version, generation, dedupe)
	return e
}
func (r *Repository) Claim(ctx context.Context) (*Job, error) {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// Expired leases may be reclaimed only within the configured attempt limit.
	_, e = tx.ExecContext(ctx, `UPDATE memory_jobs SET status='failed',last_error_code='lease_exhausted' WHERE owner_id=? AND project_id=? AND attempts>=? AND status='running' AND lease_until<NOW(6)`, r.Config.OwnerID, r.Config.ProjectID, r.Config.MaxAttempts)
	if e != nil {
		return nil, e
	}
	j := &Job{}
	e = tx.QueryRowContext(ctx, `SELECT id,kind,target,version,generation,attempts FROM memory_jobs WHERE owner_id=? AND project_id=? AND attempts<? AND ((status='pending' AND available_at<=NOW(6)) OR (status='running' AND lease_until<NOW(6))) ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, r.Config.OwnerID, r.Config.ProjectID, r.Config.MaxAttempts).Scan(&j.ID, &j.Kind, &j.Target, &j.Version, &j.Generation, &j.Attempts)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	j.Token = uuid.NewString()
	j.Attempts++
	_, e = tx.ExecContext(ctx, `UPDATE memory_jobs SET status='running',attempts=?,lease_token=?,lease_until=TIMESTAMPADD(SECOND,?,NOW(6)),updated_at=NOW(6) WHERE id=?`, j.Attempts, j.Token, int64(time.Duration(r.Config.LeaseDuration).Seconds()), j.ID)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return j, nil
}
func (r *Repository) Finish(ctx context.Context, j *Job, runErr error) error {
	status, code := "succeeded", ""
	delay := time.Duration(r.Config.RetryInitial)
	if runErr != nil {
		status = "pending"
		code = "processing_error"
		if runErr.Error() == "parse_error" || runErr.Error() == "extract_model_error" {
			code = runErr.Error()
		}
		if j.Attempts >= r.Config.MaxAttempts {
			status = "failed"
		}
		for n := 1; n < j.Attempts; n++ {
			delay *= 2
		}
		if delay > time.Duration(r.Config.RetryMax) {
			delay = time.Duration(r.Config.RetryMax)
		}
	}
	_, e := r.DB.ExecContext(ctx, `UPDATE memory_jobs SET status=?,last_error_code=?,available_at=TIMESTAMPADD(MICROSECOND,?,NOW(6)),lease_token='',lease_until=NULL,updated_at=NOW(6) WHERE id=? AND lease_token=? AND status='running'`, status, code, delay.Microseconds(), j.ID, j.Token)
	return e
}
func (r *Repository) LoadTurn(ctx context.Context, id string) (Turn, error) {
	t := Turn{ID: id}
	var raw []byte
	e := r.DB.QueryRowContext(ctx, `SELECT seq,session_id,state,suppressed,input FROM memory_turns WHERE turn_id=? AND owner_id=? AND project_id=?`, id, r.Config.OwnerID, r.Config.ProjectID).Scan(&t.Seq, &t.SessionID, &t.State, &t.Suppressed, &raw)
	if e == nil {
		e = json.Unmarshal(raw, &t.Input)
	}
	return t, e
}

const factColumns = "id,owner_id,scope_kind,scope_id,type,fact_key,value,content_hash,version,state,index_state,generation,expires_at,last_turn_seq"

type scanner interface{ Scan(...any) error }

func scanFact(row scanner) (Fact, error) {
	var f Fact
	var exp sql.NullTime
	e := row.Scan(&f.ID, &f.Scope.Owner, &f.Scope.Kind, &f.Scope.ID, &f.Type, &f.Key, &f.Value, &f.Hash, &f.Version, &f.State, &f.IndexState, &f.Generation, &exp, &f.LastTurn)
	if exp.Valid {
		f.ExpiresAt = &exp.Time
	}
	return f, e
}
func (r *Repository) Get(ctx context.Context, id string) (Fact, error) {
	return scanFact(r.DB.QueryRowContext(ctx, "SELECT "+factColumns+" FROM memory_facts WHERE id=? AND owner_id=? AND ((scope_kind='user' AND scope_id=?) OR (scope_kind='project' AND scope_id=?))", id, r.Config.OwnerID, r.Config.OwnerID, r.Config.ProjectID))
}
func (r *Repository) List(ctx context.Context) ([]Fact, error) {
	rows, e := r.DB.QueryContext(ctx, "SELECT "+factColumns+" FROM memory_facts WHERE owner_id=? AND ((scope_kind='user' AND scope_id=?) OR (scope_kind='project' AND scope_id=?)) AND state='active' AND (expires_at IS NULL OR expires_at>NOW(6)) ORDER BY updated_at DESC LIMIT 200", r.Config.OwnerID, r.Config.OwnerID, r.Config.ProjectID)
	if e != nil {
		return nil, e
	}
	out := []Fact{}
	for rows.Next() {
		f, e := scanFact(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		sr, e := r.DB.QueryContext(ctx, "SELECT source_message_id FROM memory_sources WHERE memory_id=? AND version=?", out[i].ID, out[i].Version)
		if e != nil {
			return nil, e
		}
		for sr.Next() {
			var id string
			if e = sr.Scan(&id); e != nil {
				sr.Close()
				return nil, e
			}
			out[i].Sources = append(out[i].Sources, id)
		}
		e = sr.Err()
		sr.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (r *Repository) decision(ctx context.Context, tx *sql.Tx, turn string, n int, action, reason, id string) error {
	_, e := tx.ExecContext(ctx, `INSERT IGNORE INTO memory_decisions(turn_id,ordinal,action,reason_code,memory_id,prompt_version,model) VALUES(?,?,?,?,?,?,?)`, turn, n, action, reason, id, PromptVersion, r.Model)
	return e
}
func (r *Repository) vectorized(t string) bool {
	for _, v := range r.Config.VectorizeTypes {
		if v == t {
			return true
		}
	}
	return false
}

// Apply serializes a user's memory decisions, checks the lease, and commits facts
// and index tasks atomically. Model calls happen before this short transaction.
func (r *Repository) Apply(ctx context.Context, j *Job, t Turn, cs []Candidate) error {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = r.lock(ctx, tx); e != nil {
		return e
	}
	var token string
	if e = tx.QueryRowContext(ctx, "SELECT lease_token FROM memory_jobs WHERE id=? AND status='running' AND lease_until>NOW(6) FOR UPDATE", j.ID).Scan(&token); e != nil {
		return e
	}
	if token != j.Token {
		return errors.New("lease_lost")
	}
	var state string
	var suppressed bool
	if e = tx.QueryRowContext(ctx, "SELECT state,suppressed FROM memory_turns WHERE turn_id=? FOR UPDATE", t.ID).Scan(&state, &suppressed); e != nil {
		return e
	}
	if state == "processed" || state == "deleted" {
		return nil
	}
	if state != "completed" {
		return errors.New("turn_not_completed")
	}
	if suppressed {
		cs = nil
	}
	if len(cs) == 0 {
		reason := "no_candidates"
		if suppressed {
			reason = "suppressed"
		}
		if e = r.decision(ctx, tx, t.ID, 0, "ignore", reason, ""); e != nil {
			return e
		}
	}
	for n, c := range cs {
		reason := ValidateCandidate(c, t.Input, r.Config.MaxCandidateChars)
		if reason != "" {
			if e = r.decision(ctx, tx, t.ID, n, "ignore", reason, ""); e != nil {
				return e
			}
			continue
		}
		scope := r.scope(c.ScopeKind)
		var through sql.NullInt64
		e = tx.QueryRowContext(ctx, "SELECT MAX(through_seq) FROM memory_controls WHERE owner_id=? AND scope_kind=? AND scope_id=? AND fact_key IN (?, '*')", scope.Owner, scope.Kind, scope.ID, c.Key).Scan(&through)
		if e != nil {
			return e
		}
		if through.Valid && through.Int64 >= t.Seq {
			if e = r.decision(ctx, tx, t.ID, n, "ignore", "suppressed", ""); e != nil {
				return e
			}
			continue
		}
		old, err := scanFact(tx.QueryRowContext(ctx, "SELECT "+factColumns+" FROM memory_facts WHERE owner_id=? AND scope_kind=? AND scope_id=? AND type=? AND fact_key=? FOR UPDATE", scope.Owner, scope.Kind, scope.ID, c.Type, c.Key))
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		action := "insert"
		f := Fact{ID: uuid.NewString(), Scope: scope, Type: c.Type, Key: c.Key, Value: strings.TrimSpace(c.Value), Version: 1, State: "active", Generation: r.Generation, ExpiresAt: c.ExpiresAt, LastTurn: t.Seq}
		f.Hash = Hash(Normalize(f.Value))
		f.IndexState = "not_required"
		if r.vectorized(f.Type) {
			f.IndexState = "pending"
		}
		if found {
			if old.LastTurn > t.Seq {
				if e = r.decision(ctx, tx, t.ID, n, "ignore", "stale_turn", old.ID); e != nil {
					return e
				}
				continue
			}
			oldActive := old.State == "active" && (old.ExpiresAt == nil || old.ExpiresAt.After(time.Now()))
			if oldActive && old.Hash == f.Hash && c.Relation != "retract" {
				if e = r.source(ctx, tx, old, t, c); e != nil {
					return e
				}
				if e = r.decision(ctx, tx, t.ID, n, "ignore", "duplicate", old.ID); e != nil {
					return e
				}
				continue
			}
			// A revoked/expired row is historical state, not a live competing
			// assertion. It may be reactivated by a later explicit assertion;
			// only an active row with a different value is unresolved.
			if c.Relation == "assert" && oldActive && !ExplicitCorrection(t.Input.Message.Text) {
				if e = r.decision(ctx, tx, t.ID, n, "unresolved", "conflicting_fact", old.ID); e != nil {
					return e
				}
				continue
			}
			if c.Relation == "assert" && oldActive && ExplicitCorrection(t.Input.Message.Text) {
				// Some local models omit relation=correct even when the user
				// explicitly says the previous choice was wrong. The user text is
				// the authority for this narrow replacement rule.
				c.Relation = "correct"
			}
			f.ID = old.ID
			f.Version = old.Version + 1
			action = "update"
		}
		if c.Relation == "retract" {
			if !found {
				if e = r.decision(ctx, tx, t.ID, n, "unresolved", "target_not_found", ""); e != nil {
					return e
				}
				continue
			}
			if e = r.revokeTx(ctx, tx, old, t.Seq); e != nil {
				return e
			}
			if e = r.decision(ctx, tx, t.ID, n, "revoke", "user_retracted", old.ID); e != nil {
				return e
			}
			continue
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO memory_facts(id,owner_id,scope_kind,scope_id,type,fact_key,value,content_hash,version,state,index_state,generation,expires_at,last_turn_seq) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE value=VALUES(value),content_hash=VALUES(content_hash),version=VALUES(version),state=VALUES(state),index_state=VALUES(index_state),generation=VALUES(generation),expires_at=VALUES(expires_at),last_turn_seq=VALUES(last_turn_seq),updated_at=NOW(6)`, f.ID, scope.Owner, scope.Kind, scope.ID, f.Type, f.Key, f.Value, f.Hash, f.Version, f.State, f.IndexState, f.Generation, f.ExpiresAt, t.Seq)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO memory_versions(memory_id,version,value,state,reason) VALUES(?,?,?,?,?)", f.ID, f.Version, f.Value, f.State, action); e != nil {
			return e
		}
		if e = r.source(ctx, tx, f, t, c); e != nil {
			return e
		}
		if f.IndexState == "pending" {
			if e = r.enqueue(ctx, tx, "index", f.ID, f.Version, r.Generation); e != nil {
				return e
			}
		}
		if found && old.Generation != "" {
			if e = r.enqueue(ctx, tx, "delete", VectorID(old), old.Version, old.Generation); e != nil {
				return e
			}
		}
		if e = r.decision(ctx, tx, t.ID, n, action, "confirmed_fact", f.ID); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "UPDATE memory_turns SET state='processed',updated_at=NOW(6) WHERE turn_id=?", t.ID); e != nil {
		return e
	}
	return tx.Commit()
}

// MarkIgnored closes a terminal extraction parse failure without retrying the
// same malformed model response forever. The turn remains auditable by reason.
func (r *Repository) MarkIgnored(ctx context.Context, j *Job, t Turn, reason string) error {
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = r.lock(ctx, tx); e != nil {
		return e
	}
	var token, state string
	if e = tx.QueryRowContext(ctx, "SELECT lease_token FROM memory_jobs WHERE id=? AND status='running' AND lease_until>NOW(6) FOR UPDATE", j.ID).Scan(&token); e != nil {
		return e
	}
	if token != j.Token {
		return errors.New("lease_lost")
	}
	if e = tx.QueryRowContext(ctx, "SELECT state FROM memory_turns WHERE turn_id=? FOR UPDATE", t.ID).Scan(&state); e != nil {
		return e
	}
	if state == "processed" || state == "deleted" {
		return nil
	}
	if e = r.decision(ctx, tx, t.ID, 0, "ignore", reason, ""); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE memory_turns SET state='processed',updated_at=NOW(6) WHERE turn_id=?", t.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) source(ctx context.Context, tx *sql.Tx, f Fact, t Turn, c Candidate) error {
	_, e := tx.ExecContext(ctx, "INSERT IGNORE INTO memory_sources(memory_id,version,source_message_id,turn_id,evidence) VALUES(?,?,?,?,?)", f.ID, f.Version, c.SourceID, t.ID, c.Evidence)
	return e
}

// Revocation removes evidence from all versions and suppresses queued older turns.
func (r *Repository) revokeTx(ctx context.Context, tx *sql.Tx, f Fact, seq int64) error {
	// Revoke is idempotent. A repeated DELETE must not create an endless chain
	// of tombstone revisions or enqueue duplicate cleanup work.
	if f.State == "revoked" {
		return nil
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO memory_controls(owner_id,scope_kind,scope_id,fact_key,through_seq) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE through_seq=GREATEST(through_seq,VALUES(through_seq))`, f.Scope.Owner, f.Scope.Kind, f.Scope.ID, f.Key, seq)
	if e != nil {
		return e
	}
	// The fact row advances to a tombstone version. Keep that revision in the
	// audit chain even though all historical values are redacted below.
	if _, e = tx.ExecContext(ctx, "INSERT INTO memory_versions(memory_id,version,value,state,reason) VALUES(?,?,?,?,?)", f.ID, f.Version+1, "", "revoked", "revoke"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE memory_facts SET state='revoked',value='',content_hash='',version=version+1,last_turn_seq=?,index_state='pending_delete',updated_at=NOW(6) WHERE id=?", seq, f.ID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE memory_versions SET value='',state='revoked' WHERE memory_id=?", f.ID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE memory_turns SET suppressed=TRUE,input=JSON_OBJECT('message',JSON_OBJECT('id',source_message_id,'role','user','text','')) WHERE turn_id IN (SELECT turn_id FROM memory_sources WHERE memory_id=?)`, f.ID); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM memory_sources WHERE memory_id=?", f.ID); e != nil {
		return e
	}
	// All indexed generations are recorded in the outbox, including older models.
	rows, e := tx.QueryContext(ctx, "SELECT version,generation FROM memory_jobs WHERE kind='index' AND target=?", f.ID)
	if e != nil {
		return e
	}
	var versions []Fact
	for rows.Next() {
		v := f
		if e = rows.Scan(&v.Version, &v.Generation); e != nil {
			rows.Close()
			return e
		}
		versions = append(versions, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range versions {
		if e = r.enqueue(ctx, tx, "delete", VectorID(v), v.Version, v.Generation); e != nil {
			return e
		}
	}
	return nil
}
func (r *Repository) Revoke(ctx context.Context, id string) error {
	f, e := r.Get(ctx, id)
	if e != nil {
		return e
	}
	tx, e := r.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = r.lock(ctx, tx); e != nil {
		return e
	}
	f, e = scanFact(tx.QueryRowContext(ctx, "SELECT "+factColumns+" FROM memory_facts WHERE id=? FOR UPDATE", f.ID))
	if e != nil {
		return e
	}
	var seq int64
	if e = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM memory_turns WHERE owner_id=?", r.Config.OwnerID).Scan(&seq); e != nil {
		return e
	}
	if e = r.revokeTx(ctx, tx, f, seq); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *Repository) Status(ctx context.Context, id string) (map[string]any, error) {
	t, e := r.LoadTurn(ctx, id)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"turn_id": id, "state": t.State, "suppressed": t.Suppressed}
	var status, code string
	e = r.DB.QueryRowContext(ctx, "SELECT status,last_error_code FROM memory_jobs WHERE kind='extract' AND target=?", id).Scan(&status, &code)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	out["task_status"] = status
	out["error_code"] = code
	rows, e := r.DB.QueryContext(ctx, "SELECT action,reason_code,memory_id FROM memory_decisions WHERE turn_id=? ORDER BY ordinal", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ds := []map[string]string{}
	for rows.Next() {
		var a, b, c string
		if e = rows.Scan(&a, &b, &c); e != nil {
			return nil, e
		}
		ds = append(ds, map[string]string{"action": a, "reason": b, "memory_id": c})
	}
	out["decisions"] = ds
	return out, rows.Err()
}
func (r *Repository) Retry(ctx context.Context, id string) error {
	res, e := r.DB.ExecContext(ctx, "UPDATE memory_jobs SET status='pending',attempts=0,available_at=NOW(6) WHERE id=? AND owner_id=? AND project_id=? AND status='failed'", id, r.Config.OwnerID, r.Config.ProjectID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("failed job not found")
	}
	return nil
}
