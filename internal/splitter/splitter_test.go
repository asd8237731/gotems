package splitter

import (
	"testing"

	"github.com/lyymini/gotems/internal/task"
)

func TestParseSplitResult(t *testing.T) {
	input := `好的，这是拆分结果：

[
  {
    "id": "t1",
    "prompt": "设计数据库 Schema",
    "tags": ["deep_reasoning"],
    "depends_on": []
  },
  {
    "id": "t2",
    "prompt": "实现 API 接口",
    "tags": ["code_generation"],
    "depends_on": ["t1"]
  }
]

以上是拆分结果。`

	tasks, err := parseSplitResult(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].ID != "t1" {
		t.Fatalf("expected t1, got %s", tasks[0].ID)
	}
	if tasks[1].DependsOn[0] != "t1" {
		t.Fatalf("expected t2 depends on t1, got %v", tasks[1].DependsOn)
	}
}

func TestParseSplitResultPureJSON(t *testing.T) {
	input := `[{"id":"a","prompt":"do A","tags":["code_generation"],"depends_on":[]}]`
	tasks, err := parseSplitResult(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "a" {
		t.Fatalf("unexpected result: %+v", tasks)
	}
}

func TestParseSplitResultInvalid(t *testing.T) {
	_, err := parseSplitResult("this is not json at all")
	if err == nil {
		t.Fatal("expected error for non-JSON input")
	}
}

func TestParseSplitResultEmpty(t *testing.T) {
	_, err := parseSplitResult("[]")
	if err == nil {
		t.Fatal("expected error for empty task list")
	}
}

func TestSplitterNoAgent(t *testing.T) {
	s := NewSplitter(nil, nil)
	tasks, err := s.Split(nil, "build a blog")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 fallback task, got %d", len(tasks))
	}
}

// 确保导入不报错
var _ = task.Task{}
