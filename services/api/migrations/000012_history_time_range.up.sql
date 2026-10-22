CREATE INDEX messages_group_time_seq_idx ON messages (group_id, created_at, seq);
