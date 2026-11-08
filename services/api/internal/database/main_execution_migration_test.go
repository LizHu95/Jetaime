package database

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/LizHu95/Jetaime/services/api/migrations"
)

func TestMainExecutionUpgradePreservesTasks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	steps, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	before := fstest.MapFS{}
	for _, step := range steps {
		if step.version < 16 {
			before[step.name] = &fstest.MapFile{Data: []byte(step.sql)}
		}
	}
	if err := migrate(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
 INSERT INTO users(id,nickname) VALUES('00000000-0000-0000-0000-000000000001','Alice'),('00000000-0000-0000-0000-000000000002','Bob');
 INSERT INTO chat_groups(id,user_low_id,user_high_id,last_message_seq) VALUES('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002',5);
 INSERT INTO group_members(group_id,user_id) VALUES('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001'),('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002');
 INSERT INTO group_assistants(id,group_id,agent_key) VALUES('00000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000003','main');

 INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,mentions_ai,client_message_id)
 SELECT md5('message-'||n)::uuid,'00000000-0000-0000-0000-000000000003',n,'user','00000000-0000-0000-0000-000000000001','question',true,md5('client-'||n)::uuid FROM generate_series(1,5) n;
 INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq,agent_kind)
 SELECT md5('run-'||n)::uuid,'00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000004',md5('message-'||n)::uuid,'00000000-0000-0000-0000-000000000001',n,(ARRAY['qa','help','extract','auto','main'])[n] FROM generate_series(1,5) n;
 UPDATE agent_runs SET status='failed',error_code='model_failed',finished_at=now() WHERE agent_kind='help';
 UPDATE agent_runs SET status='running',attempt_count=1,started_at=now(),lease_expires_at=now()+interval '1 minute' WHERE agent_kind='extract';
 `); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT count(*)=5 AND count(*) FILTER(WHERE agent_kind='main')=4 AND count(*) FILTER(WHERE agent_kind='auto')=1 AND count(*) FILTER(WHERE status='failed' AND error_code='model_failed')=1 AND count(*) FILTER(WHERE status='running' AND attempt_count=1 AND lease_expires_at IS NOT NULL)=1 AND bool_and(context_end_seq>0 AND trigger_message_id IS NOT NULL) FROM agent_runs`).Scan(&valid); err != nil || !valid {
		t.Fatal("task state lost", valid, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runs SET agent_kind='qa'`); err == nil {
		t.Fatal("retired mode accepted")
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal("migration not idempotent", err)
	}
}
