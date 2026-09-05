-- audit_log is now called activity_log everywhere except the schema, which
-- still had the old name. This migration only renames; no column changes.
ALTER TABLE audit_log RENAME TO activity_log;

DROP INDEX idx_audit_log_at;
CREATE INDEX idx_activity_log_at ON activity_log(at);

DROP INDEX idx_audit_log_subject;
CREATE INDEX idx_activity_log_subject ON activity_log(subject_type, subject_id);
