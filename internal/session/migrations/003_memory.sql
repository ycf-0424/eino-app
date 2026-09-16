CREATE TABLE IF NOT EXISTS memory_bindings (
 session_id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL, project_id VARCHAR(64) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_locks (
 owner_id VARCHAR(64) PRIMARY KEY
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_turns (
 seq BIGINT AUTO_INCREMENT PRIMARY KEY, turn_id VARCHAR(64) NOT NULL UNIQUE,
 session_id VARCHAR(64) NOT NULL, owner_id VARCHAR(64) NOT NULL, project_id VARCHAR(64) NOT NULL,
 source_message_id VARCHAR(64) NOT NULL UNIQUE, input JSON NOT NULL,
 state VARCHAR(24) NOT NULL, suppressed BOOLEAN NOT NULL DEFAULT FALSE,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX idx_memory_turns_session(session_id)
 ,INDEX idx_memory_turns_cleanup(owner_id,state,updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_facts (
 id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL,
 scope_kind VARCHAR(16) NOT NULL, scope_id VARCHAR(64) NOT NULL,
 type VARCHAR(24) NOT NULL, fact_key VARCHAR(128) NOT NULL,
 value TEXT NOT NULL, content_hash CHAR(64) NOT NULL,
 version BIGINT NOT NULL, state VARCHAR(24) NOT NULL,
 index_state VARCHAR(24) NOT NULL, generation VARCHAR(32) NOT NULL,
 expires_at DATETIME(6) NULL, last_turn_seq BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 UNIQUE KEY uk_memory_fact(owner_id,scope_kind,scope_id,type,fact_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_versions (
 memory_id VARCHAR(64) NOT NULL, version BIGINT NOT NULL, value TEXT NOT NULL,
 state VARCHAR(24) NOT NULL, reason VARCHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(memory_id,version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_sources (
 memory_id VARCHAR(64) NOT NULL, version BIGINT NOT NULL,
 source_message_id VARCHAR(64) NOT NULL, turn_id VARCHAR(64) NOT NULL, evidence TEXT NOT NULL,
 PRIMARY KEY(memory_id,version,source_message_id),
 INDEX idx_memory_sources_turn(turn_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_jobs (
 id VARCHAR(64) PRIMARY KEY, owner_id VARCHAR(64) NOT NULL, project_id VARCHAR(64) NOT NULL,
 kind VARCHAR(16) NOT NULL, target VARCHAR(128) NOT NULL, version BIGINT NOT NULL DEFAULT 0,
 generation VARCHAR(32) NOT NULL, dedupe_key VARCHAR(240) NOT NULL UNIQUE,
 status VARCHAR(24) NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0,
 available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 lease_until DATETIME(6) NULL, lease_token VARCHAR(64) NOT NULL DEFAULT '',
 last_error_code VARCHAR(40) NOT NULL DEFAULT '',
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX idx_memory_jobs_claim(status,available_at,lease_until),
 INDEX idx_memory_jobs_cleanup(owner_id,status,updated_at),
 INDEX idx_memory_jobs_target(kind,target,status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_controls (
 owner_id VARCHAR(64) NOT NULL, scope_kind VARCHAR(16) NOT NULL, scope_id VARCHAR(64) NOT NULL,
 fact_key VARCHAR(128) NOT NULL, through_seq BIGINT NOT NULL,
 PRIMARY KEY(owner_id,scope_kind,scope_id,fact_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS memory_decisions (
 turn_id VARCHAR(64) NOT NULL, ordinal INT NOT NULL, action VARCHAR(24) NOT NULL,
 reason_code VARCHAR(40) NOT NULL, memory_id VARCHAR(64) NOT NULL,
 prompt_version VARCHAR(32) NOT NULL, model VARCHAR(128) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 INDEX idx_memory_decisions_cleanup(created_at),
 PRIMARY KEY(turn_id,ordinal)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
