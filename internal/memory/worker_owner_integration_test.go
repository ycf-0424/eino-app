package memory

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestWorkerProcessesForeignOwnerJob 覆盖坑 B1（最致命的一条）。
//
// 改造前 Claim 把「服务端配置的 owner」写死在 SQL 里：
//
//	WHERE owner_id=? AND project_id=? AND ...
//
// 多用户上线后，B 用户对话产生的抽取 job（owner_id=B）永远匹配不到 worker 的
// 查询条件 —— 表现是 B 那边**不报任何错，记忆功能静默失效**。这里用一个
// 与配置 owner 不同的 identity 造 turn + job，验证三件事：
//
//  1. 配置里的 owner 读不到别人的 turn（归属校验仍然生效，不是靠放弃隔离修好的）；
//  2. 用 job 自带的 owner 派生 Repository 才能读到；
//  3. worker 确实领得到这条 job，并按 job 的归属把结果写进正确命名空间。
func TestWorkerProcessesForeignOwnerJob(t *testing.T) {
	r, s := integrationRepo(t)
	ctx := context.Background()
	otherOwner := "feishu:ou_" + uuid.NewString()
	turnID := uuid.NewString()

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		db := s.DB()
		db.ExecContext(cleanupCtx, "DELETE FROM memory_decisions WHERE turn_id=?", turnID)
		db.ExecContext(cleanupCtx, "DELETE FROM memory_sources WHERE owner_id=?", otherOwner)
		db.ExecContext(cleanupCtx, "DELETE FROM memory_versions WHERE owner_id=?", otherOwner)
		db.ExecContext(cleanupCtx, "DELETE FROM memory_jobs WHERE owner_id=?", otherOwner)
		db.ExecContext(cleanupCtx, "DELETE FROM memory_facts WHERE owner_id=?", otherOwner)
		db.ExecContext(cleanupCtx, "DELETE FROM memory_turns WHERE owner_id=?", otherOwner)
		db.ExecContext(cleanupCtx, "DELETE FROM memory_locks WHERE owner_id=?", otherOwner)
	})

	// 直接落库，模拟「另一个用户的会话已完成、抽取任务已入队」的状态。
	// available_at 取一年前，保证 Claim 的 ORDER BY available_at 能稳定先取到它。
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_turns(turn_id,session_id,owner_id,project_id,source_message_id,input,state,suppressed,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'completed',FALSE,NOW(6),NOW(6))`,
		turnID, uuid.NewString(), otherOwner, r.Config.ProjectID, uuid.NewString(),
		`{"message":{"id":"m1","role":"user","text":"我们决定使用 Milvus"}}`); err != nil {
		t.Fatal(err)
	}
	jobID := uuid.NewString()
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO memory_jobs(id,owner_id,project_id,kind,target,version,generation,dedupe_key,status,attempts,available_at,updated_at)
		VALUES(?,?,?,'extract',?,0,'',?,'pending',0,TIMESTAMPADD(DAY,-365,NOW(6)),NOW(6))`,
		jobID, otherOwner, r.Config.ProjectID, turnID, "b1-test-"+jobID); err != nil {
		t.Fatal(err)
	}

	// 1. 配置 owner 读不到 → 归属校验没有被绕过。
	if _, err := r.LoadTurn(ctx, turnID); err == nil {
		t.Fatal("configured owner must not read another owner's turn")
	}
	// 2. 用 job 的 owner 派生后可以读到 → 这正是 process 内部做的事。
	if _, err := r.For(otherOwner).LoadTurn(ctx, turnID); err != nil {
		t.Fatalf("derived repository must read its own turn: %v", err)
	}

	// 3. worker 领得到别人的 job，且 owner 取自 job 行本身。
	j, err := r.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("worker did not claim the foreign-owner job (B1 regression)")
	}
	if j.Owner != otherOwner || j.Target != turnID {
		t.Fatalf("claimed job = %+v, want owner=%s target=%s", j, otherOwner, turnID)
	}

	// 4. 真正跑一遍处理链路，结果必须落在 job 自己的命名空间里。
	e := &Engine{
		Repo:      r,
		Extractor: &fixedExtractor{candidates: []Candidate{{Type: "decision", ScopeKind: "project", Key: "vector_database", Value: "Milvus", Relation: "assert"}}},
		Index:     &fakeIndex{facts: map[string]Fact{}},
	}
	if err = e.process(ctx, j); err != nil {
		t.Fatalf("process foreign job: %v", err)
	}
	if err = r.Finish(ctx, j, nil); err != nil {
		t.Fatal(err)
	}
	owned, err := r.For(otherOwner).List(ctx)
	if err != nil || len(owned) != 1 || owned[0].Value != "Milvus" {
		t.Fatalf("fact not written under the job's owner: %+v %v", owned, err)
	}
	// 配置 owner 不应看到任何东西 —— 修好可领取性不能以牺牲隔离为代价。
	if leaked, err := r.List(ctx); err != nil || len(leaked) != 0 {
		t.Fatalf("configured owner sees foreign fact: %+v %v", leaked, err)
	}
}
