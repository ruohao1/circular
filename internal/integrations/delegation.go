package integrations

import (
	"context"
	"errors"
)

var errDelegationPublication = errors.New("Delegated child runs do not publish automatically")

func (s *Service) delegatedChild(ctx context.Context, run string) (bool, error) {
	var child bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delegations WHERE child_run_id=$1 AND request_key IS NOT NULL)`, run).Scan(&child)
	return child, err
}

func (s *Service) delegatedActivity(ctx context.Context, a linearActivity) (bool, error) {
	var child bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delegations d WHERE d.request_key IS NOT NULL AND
		(d.child_run_id=(SELECT run_id FROM external_requests WHERE id=$1) OR (($2 LIKE 'run:%' OR $2 LIKE 'pr:%') AND d.child_run_id::text=split_part($2,':',2))))`, a.Request, a.Semantic).Scan(&child)
	return child, err
}
