-- Run only on a dedicated test database after applying 000001_chat_schema.up.sql.
-- All fixtures are fictional and the transaction is rolled back.
BEGIN;

CREATE FUNCTION pg_temp.expect_rejected(command text, expected_state text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE actual_state text;
BEGIN
    BEGIN
        EXECUTE command;
    EXCEPTION WHEN OTHERS THEN
        GET STACKED DIAGNOSTICS actual_state = RETURNED_SQLSTATE;
        IF actual_state <> expected_state THEN
            RAISE EXCEPTION 'expected SQLSTATE %, got % for %', expected_state, actual_state, command;
        END IF;
        RETURN;
    END;
    RAISE EXCEPTION 'unexpectedly accepted: %', command;
END;
$$;

CREATE FUNCTION pg_temp.make_group(g uuid, a uuid, b uuid, ai uuid)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO chat_groups(id, user_low_id, user_high_id) VALUES (g, least(a,b), greatest(a,b));
    INSERT INTO group_members(group_id, user_id) VALUES (g,a), (g,b);
    INSERT INTO group_assistants(id, group_id, agent_key) VALUES (ai,g,'test-agent');
END;
$$;

INSERT INTO users(id, nickname) VALUES
    ('00000000-0000-0000-0000-000000000001','Alice'),
    ('00000000-0000-0000-0000-000000000002','Bob'),
    ('00000000-0000-0000-0000-000000000003','Carol');
INSERT INTO user_credentials(user_id, username, password_hash)
    VALUES ('00000000-0000-0000-0000-000000000001','alice','fixture-not-a-real-hash');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO user_credentials(user_id, username, password_hash)
    VALUES ('00000000-0000-0000-0000-000000000002','ALICE','fixture')
$q$, '23505');

-- Independent sessions survive because they are durable rows, not in-memory state.
INSERT INTO auth_sessions(id,user_id,token_hash,expires_at) VALUES
    ('10000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001',decode(repeat('01',32),'hex'),now()+interval '7 days'),
    ('10000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000001',decode(repeat('02',32),'hex'),now()+interval '7 days');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO auth_sessions(id,user_id,token_hash,expires_at) VALUES
    ('10000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001',decode('01','hex'),now()+interval '7 days')
$q$,'23514');

SELECT pg_temp.make_group('20000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002',
    '30000000-0000-0000-0000-000000000001');
SELECT pg_temp.make_group('20000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000003',
    '30000000-0000-0000-0000-000000000002');
SET CONSTRAINTS ALL IMMEDIATE;

SELECT pg_temp.expect_rejected($q$
    INSERT INTO group_members(group_id,user_id) VALUES
    ('20000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000003')
$q$,'23514');
SELECT pg_temp.expect_rejected($q$
    DELETE FROM group_members WHERE group_id='20000000-0000-0000-0000-000000000001'
$q$,'23514');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO group_assistants(id,group_id,agent_key) VALUES
    ('30000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001','other')
$q$,'23505');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO chat_groups(id,user_low_id,user_high_id) VALUES
    ('20000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002')
$q$,'23505');
SELECT pg_temp.expect_rejected($q$
    UPDATE chat_groups SET user_high_id='00000000-0000-0000-0000-000000000003'
    WHERE id='20000000-0000-0000-0000-000000000001'
$q$,'23514');
-- Immediate mode tests the same trigger executed at commit in normal creation.
SELECT pg_temp.expect_rejected($q$
    INSERT INTO chat_groups(id,user_low_id,user_high_id) VALUES
    ('20000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000003')
$q$,'23514');
SET CONSTRAINTS ALL DEFERRED;

INSERT INTO contact_requests(id,from_user_id,to_user_id) VALUES
    ('40000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000003');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO contact_requests(id,from_user_id,to_user_id) VALUES
    ('40000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000002')
$q$,'23505');
SELECT pg_temp.expect_rejected($q$
    UPDATE contact_requests SET status='accepted',resolved_at=now(),group_id='20000000-0000-0000-0000-000000000001'
    WHERE id='40000000-0000-0000-0000-000000000001'
$q$,'23503');

UPDATE chat_groups SET last_message_seq=1,last_message_at=now();
INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,mentions_ai,client_message_id) VALUES
    ('50000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001',1,'user','00000000-0000-0000-0000-000000000001','@AI find our plan',true,'60000000-0000-0000-0000-000000000001'),
    ('50000000-0000-0000-0000-000000000002','20000000-0000-0000-0000-000000000002',1,'user','00000000-0000-0000-0000-000000000003','hello',false,'60000000-0000-0000-0000-000000000002');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id) VALUES
    ('50000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001',2,'user','00000000-0000-0000-0000-000000000001','retry','60000000-0000-0000-0000-000000000001')
$q$,'23505');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id,reply_to_id) VALUES
    ('50000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001',2,'user','00000000-0000-0000-0000-000000000002','cross-group reply','60000000-0000-0000-0000-000000000003','50000000-0000-0000-0000-000000000002')
$q$,'23503');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id) VALUES
    ('50000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001',2,'user','00000000-0000-0000-0000-000000000003','outsider','60000000-0000-0000-0000-000000000003')
$q$,'23503');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content) VALUES
    ('50000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001',2,'assistant','30000000-0000-0000-0000-000000000002','wrong assistant')
$q$,'23503');

INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq) VALUES
    ('70000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001','30000000-0000-0000-0000-000000000001','50000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001',1);
SELECT pg_temp.expect_rejected($q$
    INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq) VALUES
    ('70000000-0000-0000-0000-000000000002','20000000-0000-0000-0000-000000000002','30000000-0000-0000-0000-000000000002','50000000-0000-0000-0000-000000000002','00000000-0000-0000-0000-000000000003',1)
$q$,'23503');
SELECT pg_temp.expect_rejected($q$
    UPDATE agent_runs SET requested_by='00000000-0000-0000-0000-000000000002'
    WHERE id='70000000-0000-0000-0000-000000000001'
$q$,'23503');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq) VALUES
    ('70000000-0000-0000-0000-000000000002','20000000-0000-0000-0000-000000000001','30000000-0000-0000-0000-000000000001','50000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001',1)
$q$,'23505');
SELECT pg_temp.expect_rejected($q$
    UPDATE agent_runs SET context_end_seq=2 WHERE id='70000000-0000-0000-0000-000000000001'
$q$,'23503');

UPDATE agent_runs SET status='running',attempt_count=1,started_at=now(),lease_expires_at=now()+interval '5 minutes'
    WHERE id='70000000-0000-0000-0000-000000000001';
UPDATE chat_groups SET last_message_seq=2 WHERE id='20000000-0000-0000-0000-000000000001';
INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content,reply_to_id) VALUES
    ('50000000-0000-0000-0000-000000000003','20000000-0000-0000-0000-000000000001',2,'assistant','30000000-0000-0000-0000-000000000001','shared reply','50000000-0000-0000-0000-000000000001');
UPDATE agent_runs SET status='succeeded',response_message_id='50000000-0000-0000-0000-000000000003',finished_at=now(),lease_expires_at=NULL
    WHERE id='70000000-0000-0000-0000-000000000001';
UPDATE group_members SET last_read_seq=2 WHERE group_id='20000000-0000-0000-0000-000000000001';
SELECT pg_temp.expect_rejected($q$
    UPDATE group_members SET last_read_seq=1 WHERE group_id='20000000-0000-0000-0000-000000000001'
$q$,'23514');
SELECT pg_temp.expect_rejected($q$
    UPDATE group_members SET last_read_seq=3 WHERE group_id='20000000-0000-0000-0000-000000000001'
$q$,'23514');

UPDATE chat_groups SET status='closed',closed_at=now(),closed_by='00000000-0000-0000-0000-000000000002'
    WHERE id='20000000-0000-0000-0000-000000000001';
UPDATE group_assistants SET enabled=false WHERE group_id='20000000-0000-0000-0000-000000000001';
SELECT pg_temp.expect_rejected($q$
    INSERT INTO messages(id,group_id,seq,sender_type,sender_assistant_id,content) VALUES
    ('50000000-0000-0000-0000-000000000004','20000000-0000-0000-0000-000000000001',3,'assistant','30000000-0000-0000-0000-000000000001','late reply')
$q$,'23514');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO messages(id,group_id,seq,sender_type,sender_user_id,content,client_message_id) VALUES
    ('50000000-0000-0000-0000-000000000004','20000000-0000-0000-0000-000000000001',3,'user','00000000-0000-0000-0000-000000000001','late send','60000000-0000-0000-0000-000000000004')
$q$,'23514');
SELECT pg_temp.expect_rejected($q$
    UPDATE chat_groups SET status='active',closed_at=NULL,closed_by=NULL
    WHERE id='20000000-0000-0000-0000-000000000001'
$q$,'23514');
SELECT pg_temp.expect_rejected($q$
    INSERT INTO agent_runs(id,group_id,assistant_id,trigger_message_id,requested_by,context_end_seq) VALUES
    ('70000000-0000-0000-0000-000000000002','20000000-0000-0000-0000-000000000001','30000000-0000-0000-0000-000000000001','50000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000001',1)
$q$,'23514');
-- Re-add creates a distinct conversation; the old record stays closed.
SELECT pg_temp.make_group('20000000-0000-0000-0000-000000000003',
    '00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002',
    '30000000-0000-0000-0000-000000000003');
SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;
