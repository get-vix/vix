package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/get-vix/vix/internal/protocol"
)

func (s *Session) handleTodoWrite(ctx context.Context, input map[string]any) (string, bool) {
	raw, ok := input["todos"]
	if !ok {
		return "error: todo_write requires a 'todos' array (use [] to clear the list)", true
	}

	// Round-trip through JSON so we get robust decoding from map[string]any/[]any.
	// If the model sent the array as a JSON-encoded string, unwrap it first.
	var buf []byte
	if s, ok := raw.(string); ok {
		buf = []byte(s)
	} else {
		var err error
		buf, err = json.Marshal(raw)
		if err != nil {
			return fmt.Sprintf("error: failed to encode todos: %v", err), true
		}
	}
	var items []protocol.TodoItem
	if err := json.Unmarshal(buf, &items); err != nil {
		return fmt.Sprintf("error: failed to parse todos: %v", err), true
	}
	if items == nil {
		items = []protocol.TodoItem{}
	}

	if msg := validateTodoList(items); msg != "" {
		return "error: " + msg, true
	}

	snapshot := make([]protocol.TodoItem, len(items))
	copy(snapshot, items)

	s.todoMu.Lock()
	s.todoList = snapshot
	s.todoMu.Unlock()

	emitCopy := make([]protocol.TodoItem, len(snapshot))
	copy(emitCopy, snapshot)
	s.emit("event.todo_list_updated", protocol.EventTodoListUpdated{Todos: emitCopy})

	return "TODO list updated.\n" + formatTodoList(snapshot), false
}

func (s *Session) handleTodoRead(ctx context.Context, input map[string]any) (string, bool) {
	s.todoMu.RLock()
	snapshot := make([]protocol.TodoItem, len(s.todoList))
	copy(snapshot, s.todoList)
	s.todoMu.RUnlock()
	return formatTodoList(snapshot), false
}

func validateTodoList(items []protocol.TodoItem) string {
	ids := make(map[string]int, len(items))
	for i, it := range items {
		if strings.TrimSpace(it.ID) == "" {
			return fmt.Sprintf("item at index %d has empty id", i)
		}
		if strings.TrimSpace(it.Content) == "" {
			return fmt.Sprintf("item %q has empty content", it.ID)
		}
		if !it.Status.Valid() {
			return fmt.Sprintf("item %q has invalid status %q (must be pending, in_progress, or completed)", it.ID, string(it.Status))
		}
		if _, dup := ids[it.ID]; dup {
			return fmt.Sprintf("duplicate id %q", it.ID)
		}
		ids[it.ID] = i
	}

	for _, it := range items {
		for _, dep := range it.DependsOn {
			if dep == it.ID {
				return fmt.Sprintf("item %q lists itself in depends_on", it.ID)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Sprintf("item %q depends on unknown id %q", it.ID, dep)
			}
		}
	}

	if cyc := findCycle(items, ids); cyc != "" {
		return "dependency cycle detected: " + cyc
	}

	for _, it := range items {
		if it.Status != protocol.TodoInProgress {
			continue
		}
		var blockers []string
		for _, dep := range it.DependsOn {
			depItem := items[ids[dep]]
			if depItem.Status != protocol.TodoCompleted {
				blockers = append(blockers, dep)
			}
		}
		if len(blockers) > 0 {
			return fmt.Sprintf("item %q is in_progress but depends on unfinished items: %s", it.ID, strings.Join(blockers, ", "))
		}
	}

	return ""
}

func findCycle(items []protocol.TodoItem, idx map[string]int) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, len(items))
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = -1
	}

	var cycleNodes []int
	var dfs func(u int) bool
	dfs = func(u int) bool {
		color[u] = gray
		for _, dep := range items[u].DependsOn {
			v := idx[dep]
			if color[v] == white {
				parent[v] = u
				if dfs(v) {
					return true
				}
			} else if color[v] == gray {
				cycleNodes = []int{v}
				for x := u; x != -1 && x != v; x = parent[x] {
					cycleNodes = append(cycleNodes, x)
				}
				cycleNodes = append(cycleNodes, v)
				return true
			}
		}
		color[u] = black
		return false
	}

	order := make([]int, 0, len(items))
	for i := range items {
		order = append(order, i)
	}
	sort.Ints(order)
	for _, i := range order {
		if color[i] == white {
			if dfs(i) {
				var ids []string
				for j := len(cycleNodes) - 1; j >= 0; j-- {
					ids = append(ids, items[cycleNodes[j]].ID)
				}
				return strings.Join(ids, " -> ")
			}
		}
	}
	return ""
}

func formatTodoList(items []protocol.TodoItem) string {
	if len(items) == 0 {
		return "TODO list is empty."
	}

	completed := make(map[string]bool, len(items))
	for _, it := range items {
		if it.Status == protocol.TodoCompleted {
			completed[it.ID] = true
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "TODO list (%d item%s):\n", len(items), plural(len(items)))
	for i, it := range items {
		marker := "[ ]"
		switch it.Status {
		case protocol.TodoInProgress:
			marker = "[>]"
		case protocol.TodoCompleted:
			marker = "[x]"
		}
		fmt.Fprintf(&sb, "  %s %d. %s", marker, i+1, it.Content)
		if len(it.DependsOn) > 0 {
			fmt.Fprintf(&sb, "  (depends on: %s)", strings.Join(it.DependsOn, ", "))
			if it.Status != protocol.TodoCompleted {
				blocked := false
				for _, dep := range it.DependsOn {
					if !completed[dep] {
						blocked = true
						break
					}
				}
				if blocked {
					sb.WriteString("  [blocked]")
				}
			}
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (s *Session) hasPendingTodos() bool {
	s.todoMu.RLock()
	defer s.todoMu.RUnlock()
	for _, t := range s.todoList {
		if t.Status == protocol.TodoPending || t.Status == protocol.TodoInProgress {
			return true
		}
	}
	return false
}
