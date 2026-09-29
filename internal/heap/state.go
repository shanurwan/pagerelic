package heap

// XactStatus is a transaction's fate as recorded in pg_xact.
type XactStatus uint8

const (
	StatusInProgress   XactStatus = 0
	StatusCommitted    XactStatus = 1
	StatusAborted      XactStatus = 2
	StatusSubCommitted XactStatus = 3
	StatusUnknown      XactStatus = 255
)

func (s XactStatus) String() string {
	switch s {
	case StatusInProgress:
		return "in-progress"
	case StatusCommitted:
		return "committed"
	case StatusAborted:
		return "aborted"
	case StatusSubCommitted:
		return "sub-committed"
	}
	return "unknown"
}

// XactLookup resolves transaction status. Implementations return
// StatusUnknown when they have no information.
type XactLookup interface {
	Status(xid uint32) XactStatus
}

// State is the MVCC fate of one tuple version.
type State string

const (
	Live       State = "live"        // inserted by a committed transaction, not deleted
	Deleted    State = "deleted"     // deleted by a committed transaction
	Updated    State = "updated"     // superseded by a newer version (committed UPDATE)
	Aborted    State = "aborted"     // inserted by a transaction that rolled back
	InProgress State = "in-progress" // inserter had not finished (crash, or live cluster)
	Deleting   State = "deleting"    // deleter had not finished
	Unknown    State = "unknown"     // no hint bits and no commit log to decide
	// Superseded is an unreferenced image of a row version whose header
	// still looks live: the row was changed or removed after this copy was
	// left behind (e.g. by page compaction). Only carving produces it.
	Superseded State = "superseded"
)

// Verdict explains a state.
type Verdict struct {
	State      State
	XminStatus XactStatus
	XmaxStatus XactStatus // StatusUnknown when there is no deleter
	Evidence   string     // "hint-bits", "pg_xact", "frozen", or a mix
}

// Classify determines the tuple's state. self is the tuple's own
// (block, offset) so that "deleted" can be told apart from "updated".
func Classify(h Header, selfBlock uint32, selfOffset uint16, xl XactLookup) Verdict {
	v := Verdict{XmaxStatus: StatusUnknown}
	var ev []string
	lookup := func(xid uint32) XactStatus {
		if xl == nil {
			return StatusUnknown
		}
		return xl.Status(xid)
	}

	switch {
	case h.Infomask&xminFrozen == xminFrozen || h.Xmin == FrozenXID || h.Xmin == BootstrapXID:
		v.XminStatus, ev = StatusCommitted, append(ev, "frozen")
	case h.Infomask&XminCommitted != 0:
		v.XminStatus, ev = StatusCommitted, append(ev, "hint-bits")
	case h.Infomask&XminInvalid != 0:
		v.XminStatus, ev = StatusAborted, append(ev, "hint-bits")
	default:
		if v.XminStatus = lookup(h.Xmin); v.XminStatus != StatusUnknown {
			ev = append(ev, "pg_xact")
		}
	}

	hasDeleter := h.Xmax != 0 && h.Infomask&XmaxInvalid == 0 && !h.XmaxLockedOnly()
	if hasDeleter {
		switch {
		case h.Infomask&XmaxIsMulti != 0:
			v.XmaxStatus = StatusUnknown // needs pg_multixact
			ev = append(ev, "multixact")
		case h.Infomask&XmaxCommitted != 0:
			v.XmaxStatus = StatusCommitted
			ev = append(ev, "hint-bits")
		default:
			if v.XmaxStatus = lookup(h.Xmax); v.XmaxStatus != StatusUnknown {
				ev = append(ev, "pg_xact")
			}
		}
	}
	v.Evidence = joinUnique(ev)

	switch v.XminStatus {
	case StatusAborted:
		v.State = Aborted
		return v
	case StatusInProgress:
		v.State = InProgress
		return v
	case StatusUnknown, StatusSubCommitted:
		v.State = Unknown
		return v
	}
	if !hasDeleter {
		v.State = Live
		return v
	}
	switch v.XmaxStatus {
	case StatusCommitted:
		if h.CtidBlock == selfBlock && h.CtidOffset == selfOffset {
			v.State = Deleted
		} else {
			v.State = Updated
		}
	case StatusAborted:
		v.State = Live
	case StatusInProgress:
		v.State = Deleting
	default:
		v.State = Unknown
	}
	return v
}

func joinUnique(s []string) string {
	seen := map[string]bool{}
	out := ""
	for _, x := range s {
		if seen[x] {
			continue
		}
		seen[x] = true
		if out != "" {
			out += "+"
		}
		out += x
	}
	return out
}
