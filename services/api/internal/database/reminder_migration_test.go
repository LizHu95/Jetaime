package database

import (
	"context"
	"encoding/json"
	"testing"
	"testing/fstest"

	"github.com/LizHu95/Jetaime/services/api/migrations"
)

func TestReminderDomainUpgradePreservesData(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	steps, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, step := range steps {
		if step.version < 10 {
			old[step.name] = &fstest.MapFile{Data: []byte(step.sql)}
		}
	}
	if err = migrate(ctx, pool, old); err != nil {
		t.Fatal(err)
	}
	// A complete group, two existing operations, one scheduled reminder and one
	// incomplete draft represent the pre-upgrade production schema.
	_, err = pool.Exec(ctx, `
 INSERT INTO users(id,nickname) VALUES('00000000-0000-0000-0000-000000000001','Alice'),('00000000-0000-0000-0000-000000000002','Bob');
 INSERT INTO chat_groups(id,user_low_id,user_high_id,last_message_seq) VALUES('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002',2);
 INSERT INTO group_members(group_id,user_id) VALUES('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001'),('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002');
 INSERT INTO group_assistants(id,group_id,agent_key) VALUES('00000000-0000-0000-0000-000000000004','00000000-0000-0000-0000-000000000003','main');
 INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id) VALUES
 ('00000000-0000-0000-0000-000000000005','00000000-0000-0000-0000-000000000003',1,'user','00000000-0000-0000-0000-000000000001','明天12点提醒我吃饭','00000000-0000-0000-0000-000000000005'),
 ('00000000-0000-0000-0000-000000000006','00000000-0000-0000-0000-000000000003',2,'user','00000000-0000-0000-0000-000000000002','提醒我喝水','00000000-0000-0000-0000-000000000006');
 INSERT INTO reminders(id,group_id,user_id,title,due_at,source_message_id) VALUES('00000000-0000-0000-0000-000000000007','00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001','吃饭','2026-10-21T12:00:00+08:00','00000000-0000-0000-0000-000000000005');
 INSERT INTO reminder_drafts(group_id,user_id,title,local_date) VALUES('00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002','喝水','2026-10-21');
 INSERT INTO reminder_operations(message_id,answer,needs_clarification) VALUES('00000000-0000-0000-0000-000000000005','提醒已创建',false),('00000000-0000-0000-0000-000000000006','几点提醒？',true);
 `)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var count int
	var clock, date, status string
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM reminders`).Scan(&count); err != nil || count != 2 {
		t.Fatal("data lost", count, err)
	}
	if err = pool.QueryRow(ctx, `SELECT local_date,local_time,status FROM reminders WHERE id='00000000-0000-0000-0000-000000000007'`).Scan(&date, &clock, &status); err != nil || date != "2026-10-21" || clock != "12:00" || status != "pending" {
		t.Fatal(date, clock, status, err)
	}
	var missing bool
	if err = pool.QueryRow(ctx, `SELECT local_date,local_time,due_at IS NULL FROM reminders WHERE status='draft'`).Scan(&date, &clock, &missing); err != nil || date != "2026-10-21" || clock != "" || !missing {
		t.Fatal("draft not migrated", err)
	}
	var raw []byte
	var result map[string]any
	if err = pool.QueryRow(ctx, `SELECT result FROM reminder_operations WHERE operation_id='00000000-0000-0000-0000-000000000006'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &result); err != nil || result["status"] != "needs_clarification" || result["legacyAnswer"] != "几点提醒？" {
		t.Fatal("operation lost", result, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='reminder_drafts'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("draft table remains", count, err)
	}
	if err = Migrate(ctx, pool); err != nil {
		t.Fatal("migration not idempotent", err)
	}
}
