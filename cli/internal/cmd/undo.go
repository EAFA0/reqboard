package cmd

import (
	"fmt"

	"reqboard/internal/store"
)

func runUndo(a *Args, rest []string) *Error {
	return runHistory(a, true)
}

func runRedo(a *Args, rest []string) *Error {
	return runHistory(a, false)
}

func runHistory(a *Args, undo bool) *Error {
	ctx, e := NewCtx(a)
	if e != nil {
		return e
	}
	return ctx.WithLock(func() *Error {
		var (
			snap    *store.Snapshot
			ud, rd  int
			history error
		)
		if undo {
			snap, ud, rd, history = ctx.Store.Undo()
		} else {
			snap, ud, rd, history = ctx.Store.Redo()
		}
		if history != nil {
			return fail("%v", history)
		}
		action := "undo"
		verb := "撤销"
		if !undo {
			action = "redo"
			verb = "重做"
		}
		_ = ctx.Store.AppendEvent(store.Event{
			Actor: ctx.Actor, Action: action,
			Detail: fmt.Sprintf("snapshot=%s undo_depth=%d redo_depth=%d", snap.ID, ud, rd),
		})
		text := fmt.Sprintf("[REQBOARD %s] 已%s 1 步（快照 %s，%s）\n  剩余 undo=%d redo=%d\n",
			action, verb, snap.ID, snap.TS, ud, rd)
		ctx.Emit(text, map[string]interface{}{
			"ok": true, "action": action, "snapshot_id": snap.ID, "snapshot_ts": snap.TS,
			"undo_depth": ud, "redo_depth": rd,
		})
		return nil
	})
}
