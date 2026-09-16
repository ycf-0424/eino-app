package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type Engine struct {
	Repo      *Repository
	Extractor Extractor
	Index     MemoryIndex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	Processed atomic.Int64
	Failed    atomic.Int64
}

// maintenanceSnapshot is intentionally based on information_schema for table
// size. InnoDB's TABLE_ROWS is an estimate, but it is cheap enough to sample
// on every maintenance run and is useful for spotting unbounded growth.
type maintenanceSnapshot struct {
	Tables       map[string]maintenanceTable `json:"tables"`
	ActiveFacts  int64                       `json:"active_facts"`
	PendingJobs  int64                       `json:"pending_jobs"`
	TerminalJobs int64                       `json:"terminal_jobs"`
}

type maintenanceTable struct {
	Rows  int64 `json:"rows"`
	Bytes int64 `json:"bytes"`
}

type maintenanceReport struct {
	Before              *maintenanceSnapshot `json:"before,omitempty"`
	After               *maintenanceSnapshot `json:"after,omitempty"`
	Deleted             map[string]int64     `json:"deleted,omitempty"`
	Redacted            map[string]int64     `json:"redacted,omitempty"`
	Queued              map[string]int64     `json:"queued,omitempty"`
	ExpiredFactsRevoked int64                `json:"expired_facts_revoked,omitempty"`
	DurationMS          int64                `json:"duration_ms"`
	SnapshotError       string               `json:"snapshot_error,omitempty"`
	Error               string               `json:"error,omitempty"`
}

func newMaintenanceReport() *maintenanceReport {
	return &maintenanceReport{
		Deleted:  map[string]int64{},
		Redacted: map[string]int64{},
		Queued:   map[string]int64{},
	}
}

func (r *maintenanceReport) add(bucket map[string]int64, key string, n int64) {
	if n > 0 {
		bucket[key] += n
	}
}

func (e *Engine) maintenanceExec(ctx context.Context, report *maintenanceReport, bucket, table, query string, args ...any) error {
	res, err := e.Repo.DB.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	switch bucket {
	case "deleted":
		report.add(report.Deleted, table, n)
	case "redacted":
		report.add(report.Redacted, table, n)
	case "queued":
		report.add(report.Queued, table, n)
	}
	return nil
}

func (e *Engine) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	e.cancel = cancel
	for n := 0; n < e.Repo.Config.Workers; n++ {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					for ctx.Err() == nil {
						worked, err := e.ProcessOne(ctx)
						if err != nil {
							log.Printf("memory worker: task failed (details withheld)")
						}
						if !worked {
							break
						}
					}
				}
			}
		}()
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.Maintain(ctx); err != nil {
			log.Print("memory maintenance failed")
		}
		interval := time.Hour
		if e.Repo.Config.CleanupInterval > 0 {
			interval = time.Duration(e.Repo.Config.CleanupInterval)
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := e.Maintain(ctx); err != nil {
					log.Print("memory maintenance failed")
				}
			}
		}
	}()
}
func (e *Engine) Close() error {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
	if c, ok := e.Index.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
func (e *Engine) ProcessOne(parent context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(e.Repo.Config.TaskTimeout))
	defer cancel()
	j, err := e.Repo.Claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	err = e.process(ctx, j)
	done, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	finishErr := e.Repo.Finish(done, j, err)
	if err != nil {
		e.Failed.Add(1)
	} else {
		e.Processed.Add(1)
	}
	return true, errors.Join(err, finishErr)
}
func (e *Engine) process(ctx context.Context, j *Job) error {
	switch j.Kind {
	case "extract":
		t, err := e.Repo.LoadTurn(ctx, j.Target)
		if err != nil {
			return err
		}
		if t.State == "processed" || t.State == "deleted" {
			return nil
		}
		var cs []Candidate
		if !t.Suppressed {
			t.Input.Existing, err = e.Repo.List(ctx)
			if err != nil {
				return err
			}
			if len(t.Input.Existing) > 30 {
				t.Input.Existing = t.Input.Existing[:30]
			}
			cs, err = e.Extractor.Extract(ctx, t.Input)
			if err != nil {
				if err.Error() == "parse_error" {
					return e.Repo.MarkIgnored(ctx, j, t, "parse_error")
				}
				return err
			}
		}
		return e.Repo.Apply(ctx, j, t, cs)
	case "index":
		f, err := e.Repo.Get(ctx, j.Target)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		id := f.ID + "_" + itoa(j.Version) + "_" + j.Generation
		if j.Generation != e.Repo.Generation {
			return nil
		}
		if f.State != "active" || f.Version != j.Version || f.Generation != j.Generation || f.ExpiresAt != nil && !f.ExpiresAt.After(time.Now()) {
			return e.Index.DeleteVersions(ctx, []string{id})
		}
		if err = e.Index.UpsertVersion(ctx, f); err != nil {
			return err
		}
		// Recheck after IO: deletion or a new revision may have committed meanwhile.
		current, err := e.Repo.Get(ctx, f.ID)
		if err != nil {
			return err
		}
		if current.State != "active" || current.Version != f.Version || current.Generation != f.Generation {
			return e.Index.DeleteVersions(ctx, []string{id})
		}
		_, err = e.Repo.DB.ExecContext(ctx, "UPDATE memory_facts SET index_state='indexed' WHERE id=? AND version=? AND generation=? AND state='active'", f.ID, f.Version, f.Generation)
		return err
	case "delete":
		return e.Index.DeleteVersions(ctx, []string{j.Target})
	default:
		return errors.New("unknown_job")
	}
}
func (e *Engine) snapshotMaintenance(ctx context.Context) (maintenanceSnapshot, error) {
	snapshot := maintenanceSnapshot{Tables: map[string]maintenanceTable{}}
	rows, err := e.Repo.DB.QueryContext(ctx, `SELECT table_name,
		COALESCE(table_rows,0),
		COALESCE(data_length,0)+COALESCE(index_length,0)
		FROM information_schema.tables
		WHERE table_schema=DATABASE()
		  AND table_name IN ('conversations','messages','memory_turns','memory_facts',
			'memory_versions','memory_sources','memory_jobs','memory_decisions',
			'memory_bindings','memory_controls','memory_locks')`)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var name string
		var table maintenanceTable
		if err = rows.Scan(&name, &table.Rows, &table.Bytes); err != nil {
			rows.Close()
			return snapshot, err
		}
		snapshot.Tables[name] = table
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return snapshot, err
	}
	rows.Close()
	if err = e.Repo.DB.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM memory_facts
		WHERE owner_id=? AND state='active'
		  AND (expires_at IS NULL OR expires_at>NOW(6))`, e.Repo.Config.OwnerID).Scan(&snapshot.ActiveFacts); err != nil {
		return snapshot, err
	}
	if err = e.Repo.DB.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM memory_jobs
		WHERE owner_id=? AND status IN ('pending','running')`, e.Repo.Config.OwnerID).Scan(&snapshot.PendingJobs); err != nil {
		return snapshot, err
	}
	if err = e.Repo.DB.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM memory_jobs
		WHERE owner_id=? AND status IN ('succeeded','failed')`, e.Repo.Config.OwnerID).Scan(&snapshot.TerminalJobs); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (e *Engine) Maintain(ctx context.Context) (err error) {
	started := time.Now()
	report := newMaintenanceReport()
	if before, snapshotErr := e.snapshotMaintenance(ctx); snapshotErr != nil {
		report.SnapshotError = snapshotErr.Error()
	} else {
		report.Before = &before
	}
	defer func() {
		if after, snapshotErr := e.snapshotMaintenance(ctx); snapshotErr != nil {
			if report.SnapshotError == "" {
				report.SnapshotError = snapshotErr.Error()
			} else {
				report.SnapshotError += "; after: " + snapshotErr.Error()
			}
		} else {
			report.After = &after
		}
		report.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			report.Error = err.Error()
		}
		if encoded, marshalErr := json.Marshal(report); marshalErr == nil {
			log.Printf("memory maintenance: %s", encoded)
		} else {
			log.Printf("memory maintenance report failed: %v", marshalErr)
		}
	}()
	rows, err := e.Repo.DB.QueryContext(ctx, "SELECT id FROM memory_facts WHERE owner_id=? AND state='active' AND expires_at<=NOW(6)", e.Repo.Config.OwnerID)
	if err != nil {
		return fmt.Errorf("find expired facts: %w", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("read expired facts: %w", err)
	}
	for _, id := range ids {
		if err = e.Repo.Revoke(ctx, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		report.ExpiredFactsRevoked++
	}
	batch := e.Repo.Config.CleanupBatchSize
	if batch < 1 {
		batch = 500
	}
	err = e.maintenanceExec(ctx, report, "deleted", "memory_decisions", `DELETE FROM memory_decisions
		WHERE created_at<TIMESTAMPADD(DAY,?,NOW(6))
		  AND EXISTS (SELECT 1 FROM memory_turns t WHERE t.turn_id=memory_decisions.turn_id AND t.owner_id=?)
		ORDER BY created_at LIMIT ?`, -e.Repo.Config.RetentionDays, e.Repo.Config.OwnerID, batch)
	if err != nil {
		return fmt.Errorf("cleanup decisions: %w", err)
	}
	err = e.maintenanceExec(ctx, report, "redacted", "memory_turns", `UPDATE memory_turns SET input=JSON_OBJECT('message',JSON_OBJECT('id',source_message_id,'role','user','text','')) WHERE owner_id=? AND state IN ('processed','deleted','failed','cancelled') AND updated_at<TIMESTAMPADD(DAY,?,NOW(6)) LIMIT ?`, e.Repo.Config.OwnerID, -e.Repo.Config.TurnRetentionDays, batch)
	if err != nil {
		return fmt.Errorf("redact turn snapshots: %w", err)
	}
	err = e.maintenanceExec(ctx, report, "deleted", "memory_turns", `DELETE FROM memory_turns WHERE owner_id=? AND state IN ('processed','deleted','failed','cancelled') AND updated_at<TIMESTAMPADD(DAY,?,NOW(6)) AND NOT EXISTS (SELECT 1 FROM memory_jobs j WHERE j.kind='extract' AND j.target=memory_turns.turn_id AND j.status IN ('pending','running')) AND NOT EXISTS (SELECT 1 FROM memory_decisions d WHERE d.turn_id=memory_turns.turn_id) LIMIT ?`, e.Repo.Config.OwnerID, -e.Repo.Config.TurnRetentionDays, batch)
	if err != nil {
		return err
	}
	keep := e.Repo.Config.VersionKeepCount
	if keep < 1 {
		keep = 3
	}
	versionsDeleted, deleteJobsQueued, err := e.cleanupVersions(ctx, batch, keep)
	report.add(report.Deleted, "memory_versions", versionsDeleted)
	report.add(report.Queued, "memory_jobs.delete", deleteJobsQueued)
	if err != nil {
		return fmt.Errorf("cleanup versions: %w", err)
	}
	sourcesDeleted, err := e.cleanupSources(ctx, batch, e.Repo.Config.SourceRetentionDays)
	report.add(report.Deleted, "memory_sources", sourcesDeleted)
	if err != nil {
		return fmt.Errorf("cleanup sources: %w", err)
	}
	// Keep successful index jobs for historical/revoked versions until
	// cleanupVersions has captured their generation and queued Milvus deletion.
	// Current active versions do not need the old outbox row after indexing.
	indexSucceededDeleted, err := e.cleanupIndexJobs(ctx, batch, e.Repo.Config.JobSuccessDays, "succeeded")
	report.add(report.Deleted, "memory_jobs.index_succeeded", indexSucceededDeleted)
	if err != nil {
		return fmt.Errorf("cleanup succeeded index jobs: %w", err)
	}
	indexFailedDeleted, err := e.cleanupIndexJobs(ctx, batch, e.Repo.Config.JobFailedDays, "failed")
	report.add(report.Deleted, "memory_jobs.index_failed", indexFailedDeleted)
	if err != nil {
		return fmt.Errorf("cleanup failed index jobs: %w", err)
	}
	err = e.maintenanceExec(ctx, report, "deleted", "memory_jobs.terminal_succeeded", `DELETE FROM memory_jobs WHERE owner_id=? AND kind IN ('extract','delete') AND status='succeeded' AND updated_at<TIMESTAMPADD(DAY,?,NOW(6)) LIMIT ?`, e.Repo.Config.OwnerID, -e.Repo.Config.JobSuccessDays, batch)
	if err != nil {
		return fmt.Errorf("cleanup succeeded jobs: %w", err)
	}
	err = e.maintenanceExec(ctx, report, "deleted", "memory_jobs.terminal_failed", `DELETE FROM memory_jobs WHERE owner_id=? AND kind IN ('extract','delete') AND status='failed' AND attempts>=? AND updated_at<TIMESTAMPADD(DAY,?,NOW(6)) LIMIT ?`, e.Repo.Config.OwnerID, e.Repo.Config.MaxAttempts, -e.Repo.Config.JobFailedDays, batch)
	if err != nil {
		return fmt.Errorf("cleanup failed jobs: %w", err)
	}
	err = e.maintenanceExec(ctx, report, "deleted", "memory_bindings", `DELETE FROM memory_bindings WHERE NOT EXISTS (SELECT 1 FROM conversations c WHERE c.id=memory_bindings.session_id) LIMIT ?`, batch)
	if err != nil {
		return fmt.Errorf("cleanup bindings: %w", err)
	}
	return nil
}

func (e *Engine) cleanupIndexJobs(ctx context.Context, batch, retentionDays int, status string) (int64, error) {
	tx, err := e.Repo.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	query := `SELECT j.id
		FROM memory_jobs j
		LEFT JOIN memory_facts f ON f.id=j.target AND f.owner_id=j.owner_id AND f.state='active' AND f.version=j.version
		LEFT JOIN memory_versions v ON v.memory_id=j.target AND v.version=j.version
		LEFT JOIN memory_jobs d ON d.owner_id=j.owner_id AND d.kind='delete' AND d.target=CONCAT(j.target,'_',j.version,'_',j.generation) AND d.status='succeeded'
		WHERE j.owner_id=? AND j.kind='index' AND j.status=? AND j.updated_at<TIMESTAMPADD(DAY,?,NOW(6))
		  AND (j.status='succeeded' OR j.attempts>=?)
		  AND ((f.id IS NOT NULL AND f.index_state='indexed') OR v.memory_id IS NULL OR d.id IS NOT NULL)
		GROUP BY j.id ORDER BY j.updated_at LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, e.Repo.Config.OwnerID, status, -retentionDays, e.Repo.Config.MaxAttempts, batch)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, "DELETE FROM memory_jobs WHERE id=?", id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

// cleanupVersions selects version keys before deleting them. This avoids
// MySQL's target-table restriction for DELETE statements with self-subqueries
// and keeps each cleanup transaction bounded.
func (e *Engine) cleanupVersions(ctx context.Context, batch, keep int) (int64, int64, error) {
	tx, err := e.Repo.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT v.memory_id,v.version
		FROM memory_versions v
		JOIN memory_facts f ON f.id=v.memory_id AND f.owner_id=?
		WHERE v.version <= f.version-?
		ORDER BY v.memory_id,v.version LIMIT ?`, e.Repo.Config.OwnerID, keep, batch)
	if err != nil {
		return 0, 0, err
	}
	type versionKey struct {
		id      string
		version int64
	}
	keys := make([]versionKey, 0, batch)
	for rows.Next() {
		var k versionKey
		if err = rows.Scan(&k.id, &k.version); err != nil {
			rows.Close()
			return 0, 0, err
		}
		keys = append(keys, k)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	deleted := int64(0)
	queued := int64(0)
	for _, k := range keys {
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_jobs WHERE owner_id=? AND kind='index' AND target=? AND version=? AND status IN ('pending','running')`, e.Repo.Config.OwnerID, k.id, k.version).Scan(&pending); err != nil {
			return deleted, queued, err
		}
		if pending > 0 {
			continue
		}
		indexRows, err := tx.QueryContext(ctx, `SELECT generation FROM memory_jobs WHERE owner_id=? AND kind='index' AND target=? AND version=?`, e.Repo.Config.OwnerID, k.id, k.version)
		if err != nil {
			return deleted, queued, err
		}
		var generations []string
		for indexRows.Next() {
			var generation string
			if err = indexRows.Scan(&generation); err != nil {
				indexRows.Close()
				return deleted, queued, err
			}
			if generation != "" {
				generations = append(generations, generation)
			}
		}
		if err = indexRows.Err(); err != nil {
			indexRows.Close()
			return deleted, queued, err
		}
		indexRows.Close()
		for _, generation := range generations {
			if err = e.Repo.enqueue(ctx, tx, "delete", k.id+"_"+itoa(k.version)+"_"+generation, k.version, generation); err != nil {
				return deleted, queued, err
			}
			queued++
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM memory_sources WHERE memory_id=? AND version=?", k.id, k.version); err != nil {
			return deleted, queued, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM memory_versions WHERE memory_id=? AND version=?", k.id, k.version); err != nil {
			return deleted, queued, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM memory_jobs WHERE owner_id=? AND kind='index' AND target=? AND version=? AND status NOT IN ('pending','running')", e.Repo.Config.OwnerID, k.id, k.version); err != nil {
			return deleted, queued, err
		}
		deleted++
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return deleted, queued, nil
}

// cleanupSources removes evidence for historical/revoked versions after its
// retention window. Evidence for the current active version is retained so a
// live fact still has an auditable source.
func (e *Engine) cleanupSources(ctx context.Context, batch, retentionDays int) (int64, error) {
	if retentionDays < 1 {
		retentionDays = 90
	}
	tx, err := e.Repo.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT s.memory_id,s.version,s.source_message_id
		FROM memory_sources s
		LEFT JOIN memory_facts f ON f.id=s.memory_id AND f.owner_id=?
		LEFT JOIN memory_turns t ON t.turn_id=s.turn_id
		WHERE (f.id IS NULL OR f.state<>'active' OR f.version<>s.version)
		  AND (t.turn_id IS NULL OR t.updated_at<TIMESTAMPADD(DAY,?,NOW(6)))
		ORDER BY s.memory_id,s.version,s.source_message_id LIMIT ?`, e.Repo.Config.OwnerID, -retentionDays, batch)
	if err != nil {
		return 0, err
	}
	type sourceKey struct {
		id      string
		version int64
		source  string
	}
	keys := make([]sourceKey, 0, batch)
	for rows.Next() {
		var k sourceKey
		if err = rows.Scan(&k.id, &k.version, &k.source); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, k := range keys {
		if _, err = tx.ExecContext(ctx, "DELETE FROM memory_sources WHERE memory_id=? AND version=? AND source_message_id=?", k.id, k.version, k.source); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(keys)), nil
}
