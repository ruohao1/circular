// Package runstate owns the language-independent Run transition rules.
package runstate

import "fmt"

type Status string

const (
	Queued             Status = "queued"
	Provisioning       Status = "provisioning"
	Running            Status = "running"
	WaitingForApproval Status = "waiting_for_approval"
	WaitingForInput    Status = "waiting_for_input"
	Finalizing         Status = "finalizing"
	Succeeded          Status = "succeeded"
	Failed             Status = "failed"
	Cancelled          Status = "cancelled"
)

func (s Status) Valid() bool {
	switch s {
	case Queued, Provisioning, Running, WaitingForApproval, WaitingForInput,
		Finalizing, Succeeded, Failed, Cancelled:
		return true
	}
	return false
}

func (s Status) Terminal() bool {
	return s == Succeeded || s == Failed || s == Cancelled
}

func Validate(current, target Status) error {
	allowed := false
	switch current {
	case Queued:
		allowed = target == Provisioning || target == Cancelled
	case Provisioning, WaitingForApproval, WaitingForInput:
		allowed = target == Running || target == Failed || target == Cancelled
	case Running:
		allowed = target == WaitingForApproval || target == WaitingForInput ||
			target == Finalizing || target == Failed || target == Cancelled
	case Finalizing:
		allowed = target == Succeeded || target == Failed || target == Cancelled
	}
	if !allowed {
		return fmt.Errorf("run cannot transition from %s to %s", current, target)
	}
	return nil
}
