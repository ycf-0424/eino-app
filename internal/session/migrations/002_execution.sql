CREATE TABLE IF NOT EXISTS execution_runs (
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 conversation_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 user_sequence BIGINT NULL,
 assistant_sequence BIGINT NULL,
 status VARCHAR(24) NOT NULL,
 started_at DATETIME(6) NOT NULL,
 finished_at DATETIME(6) NULL,
 INDEX idx_execution_runs_conversation(conversation_id, started_at),
 CONSTRAINT fk_execution_runs_conversation FOREIGN KEY(conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE IF NOT EXISTS execution_events (
 event_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 run_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 sequence BIGINT NOT NULL,
 type VARCHAR(40) NOT NULL,
 occurred_at DATETIME(6) NOT NULL,
 payload JSON NOT NULL,
 UNIQUE KEY uk_execution_events_run_sequence(run_id, sequence),
 INDEX idx_execution_events_occurred(occurred_at),
 CONSTRAINT fk_execution_events_run FOREIGN KEY(run_id) REFERENCES execution_runs(run_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
