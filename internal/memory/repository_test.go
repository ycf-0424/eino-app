package memory

import (
	"testing"

	"my-eino-app/internal/config"
)

// For 是 memory 包多用户隔离的唯一入口：31 处读取都靠它拿到正确的 owner。
// 这里固定住它的三条契约：只改派生副本的 owner、其余配置原样保留、
// 原对象不被修改（否则并发使用同一 Repository 会互相串号）。
func TestForDerivesOwnerWithoutMutatingBase(t *testing.T) {
	base := &Repository{
		Config:     config.Memory{OwnerID: "local:origin", ProjectID: "proj", TopK: 5},
		Generation: "gen123",
		Model:      "test-model",
	}
	derived := base.For("feishu:ou_other")

	if derived.Config.OwnerID != "feishu:ou_other" {
		t.Fatalf("derived owner = %q", derived.Config.OwnerID)
	}
	if base.Config.OwnerID != "local:origin" {
		t.Fatalf("base owner was mutated: %q", base.Config.OwnerID)
	}
	if derived.Config.ProjectID != "proj" || derived.Config.TopK != 5 {
		t.Fatalf("project config not preserved: %+v", derived.Config)
	}
	if derived.DB != base.DB || derived.Generation != base.Generation || derived.Model != base.Model {
		t.Fatal("shared fields must be carried over unchanged")
	}
	if derived == base {
		t.Fatal("For must return a distinct Repository, not the receiver")
	}
}

// scope 决定 SQL 里 owner_id / scope_id 的取值，派生后用户域必须指向新 owner，
// 项目域继续指向 project_id（项目是所有用户共享的命名空间）。
func TestForChangesUserScopeOnly(t *testing.T) {
	base := &Repository{Config: config.Memory{OwnerID: "local:origin", ProjectID: "proj"}}
	derived := base.For("feishu:ou_other")

	user := derived.scope("user")
	if user.ID != "feishu:ou_other" || user.Owner != "feishu:ou_other" {
		t.Fatalf("user scope = %+v", user)
	}
	project := derived.scope("project")
	if project.ID != "proj" || project.Owner != "feishu:ou_other" {
		t.Fatalf("project scope = %+v", project)
	}
}

// ContextFor 在 Engine 为空时必须返回 nil，服务层的 s.memories != nil 判断
// 不能因为这里 panic 而失效；非空时必须给出可调用的函数。
func TestContextForNilEngine(t *testing.T) {
	var e *Engine
	if e.ContextFor("feishu:ou_x") != nil {
		t.Fatal("nil engine must yield a nil context func")
	}
	e = &Engine{Repo: &Repository{Config: config.Memory{OwnerID: "local:origin"}}}
	if e.ContextFor("feishu:ou_bound") == nil {
		t.Fatal("expected a non-nil context func")
	}
}
