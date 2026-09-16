package memory

import (
	"context"
	"database/sql"
	"errors"
)

// Reindex paginates SQL, not a bounded List of recent memories. A failed/succeeded
// current job can be requeued, but a running lease is never stolen.
func (r *Repository) Reindex(ctx context.Context, execute bool) (int, error) {
	cursor := ""
	count := 0
	for {
		rows, err := r.DB.QueryContext(ctx, "SELECT "+factColumns+" FROM memory_facts WHERE owner_id=? AND ((scope_kind='user' AND scope_id=?) OR (scope_kind='project' AND scope_id=?)) AND id>? AND state='active' AND (expires_at IS NULL OR expires_at>NOW(6)) ORDER BY id LIMIT 100", r.Config.OwnerID, r.Config.OwnerID, r.Config.ProjectID, cursor)
		if err != nil {
			return count, err
		}
		batch := []Fact{}
		for rows.Next() {
			f, e := scanFact(rows)
			if e != nil {
				rows.Close()
				return count, e
			}
			batch = append(batch, f)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return count, err
		}
		if len(batch) == 0 {
			return count, nil
		}
		for _, f := range batch {
			cursor = f.ID
			if !r.vectorized(f.Type) {
				continue
			}
			count++
			if !execute {
				continue
			}
			err = func() error {
				tx, e := r.DB.BeginTx(ctx, nil)
				if e != nil {
					return e
				}
				defer tx.Rollback()
				if e = r.lock(ctx, tx); e != nil {
					return e
				}
				current, e := scanFact(tx.QueryRowContext(ctx, "SELECT "+factColumns+" FROM memory_facts WHERE id=? FOR UPDATE", f.ID))
				if errors.Is(e, sql.ErrNoRows) {
					return nil
				}
				if e != nil {
					return e
				}
				if current.State != "active" {
					return nil
				}
				oldGeneration := current.Generation
				if _, e = tx.ExecContext(ctx, "UPDATE memory_facts SET generation=?,index_state='pending' WHERE id=?", r.Generation, f.ID); e != nil {
					return e
				}
				if oldGeneration != "" && oldGeneration != r.Generation {
					if e = r.enqueue(ctx, tx, "delete", VectorID(Fact{ID: current.ID, Version: current.Version, Generation: oldGeneration}), current.Version, oldGeneration); e != nil {
						return e
					}
				}
				if e = r.enqueue(ctx, tx, "index", f.ID, current.Version, r.Generation); e != nil {
					return e
				}
				_, e = tx.ExecContext(ctx, "UPDATE memory_jobs SET status='pending',attempts=0,available_at=NOW(6) WHERE kind='index' AND target=? AND version=? AND generation=? AND status IN ('failed','succeeded')", f.ID, current.Version, r.Generation)
				if e != nil {
					return e
				}
				return tx.Commit()
			}()
			if err != nil {
				return count, err
			}
		}
	}
}
