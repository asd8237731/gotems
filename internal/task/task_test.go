package task

import (
	"testing"
)

func TestPoolAddAndClaim(t *testing.T) {
	pool := NewPool()

	pool.Add(&Task{ID: "t1", Prompt: "task 1"})
	pool.Add(&Task{ID: "t2", Prompt: "task 2"})

	if pool.Pending() != 2 {
		t.Fatalf("expected 2 pending tasks, got %d", pool.Pending())
	}

	claimed := pool.Claim("agent-1")
	if claimed == nil {
		t.Fatal("expected to claim a task, got nil")
	}
	if claimed.AssignedTo != "agent-1" {
		t.Fatalf("expected assigned to agent-1, got %s", claimed.AssignedTo)
	}
	if claimed.Status != TaskAssigned {
		t.Fatalf("expected status assigned, got %s", claimed.Status)
	}

	if pool.Pending() != 1 {
		t.Fatalf("expected 1 pending task after claim, got %d", pool.Pending())
	}
}

func TestPoolComplete(t *testing.T) {
	pool := NewPool()
	pool.Add(&Task{ID: "t1", Prompt: "task 1"})

	pool.Claim("agent-1")
	pool.Complete("t1", "done result")

	task := pool.Get("t1")
	if task.Status != TaskCompleted {
		t.Fatalf("expected completed, got %s", task.Status)
	}
	if task.Result != "done result" {
		t.Fatalf("expected result 'done result', got %s", task.Result)
	}
}

func TestPoolDependencies(t *testing.T) {
	pool := NewPool()
	pool.Add(&Task{ID: "t1", Prompt: "first"})
	pool.Add(&Task{ID: "t2", Prompt: "second", DependsOn: []string{"t1"}})

	// t2 依赖 t1，不能先领取 t2
	claimed := pool.Claim("agent-1")
	if claimed.ID != "t1" {
		t.Fatalf("expected t1 to be claimed first, got %s", claimed.ID)
	}

	// t1 未完成，t2 还不能领取
	claimed2 := pool.Claim("agent-2")
	if claimed2 != nil {
		t.Fatal("expected nil since t2 depends on uncompleted t1")
	}

	// 完成 t1
	pool.Complete("t1", "result 1")

	// 现在可以领取 t2
	claimed3 := pool.Claim("agent-2")
	if claimed3 == nil || claimed3.ID != "t2" {
		t.Fatal("expected t2 to be claimable after t1 completed")
	}
}

func TestPoolFail(t *testing.T) {
	pool := NewPool()
	pool.Add(&Task{ID: "t1", Prompt: "task"})
	pool.Claim("agent-1")
	pool.Fail("t1", "something went wrong")

	task := pool.Get("t1")
	if task.Status != TaskFailed {
		t.Fatalf("expected failed, got %s", task.Status)
	}
	if task.Error != "something went wrong" {
		t.Fatalf("unexpected error message: %s", task.Error)
	}
}

func TestPoolAll(t *testing.T) {
	pool := NewPool()
	pool.Add(&Task{ID: "a", Prompt: "1"})
	pool.Add(&Task{ID: "b", Prompt: "2"})
	pool.Add(&Task{ID: "c", Prompt: "3"})

	all := pool.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(all))
	}
}
