-- 006：显式上传附件及其可追溯解析产物。原文件始终保存在 app-data/filesystem。
CREATE TABLE IF NOT EXISTS attachments (
 id CHAR(36) PRIMARY KEY,
 owner_id VARCHAR(191) NOT NULL DEFAULT '',
 original_name VARCHAR(255) NOT NULL,
 sha256 CHAR(64) NOT NULL,
 mime_type VARCHAR(127) NOT NULL,
 size_bytes BIGINT UNSIGNED NOT NULL,
 storage_key VARCHAR(255) NOT NULL,
 status VARCHAR(16) NOT NULL,
 error_reason VARCHAR(512) NOT NULL DEFAULT '',
 retained_until DATETIME(6) NOT NULL,
 parser_version VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
 UNIQUE KEY uniq_attachments_storage_key (storage_key),
 KEY idx_attachments_owner_created (owner_id, created_at),
 KEY idx_attachments_retention (status, retained_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS attachment_artifacts (
 id CHAR(36) PRIMARY KEY,
 attachment_id CHAR(36) NOT NULL,
 owner_id VARCHAR(191) NOT NULL DEFAULT '',
 source_ref VARCHAR(512) NOT NULL,
 text MEDIUMTEXT NOT NULL,
 blocks JSON NULL,
 ocr_confidence DECIMAL(5,4) NULL,
 confidence DECIMAL(5,4) NULL,
 extractor_version VARCHAR(64) NOT NULL,
 content_hash CHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 KEY idx_attachment_artifacts_owner (owner_id, attachment_id),
 CONSTRAINT fk_attachment_artifacts_attachment FOREIGN KEY (attachment_id) REFERENCES attachments(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
