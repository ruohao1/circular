package integrations

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type LinearTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}
type LinearProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type LinearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	State       struct {
		Name string `json:"name"`
	} `json:"state"`
}
type CursorPage[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}
type linearPage[T any] struct {
	Nodes    []T `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

func (p linearPage[T]) public() CursorPage[T] {
	items := p.Nodes
	if items == nil {
		items = []T{}
	}
	next := ""
	if p.PageInfo.HasNextPage {
		next = p.PageInfo.EndCursor
	}
	return CursorPage[T]{Items: items, NextCursor: next}
}

func (s *Service) LinearTeams(ctx context.Context, project, cursor string) (CursorPage[LinearTeam], error) {
	var data struct {
		Teams linearPage[LinearTeam] `json:"teams"`
	}
	err := s.withToken(ctx, project, "linear", func(token string) error {
		return s.linear(ctx, token, `query CircularTeams($after: String) { teams(first: 100, after: $after) { nodes { id name key } pageInfo { hasNextPage endCursor } } }`, cursorVariables(cursor), &data)
	})
	return data.Teams.public(), err
}

func (s *Service) LinearProjects(ctx context.Context, project, cursor string) (CursorPage[LinearProject], error) {
	var data struct {
		Projects linearPage[LinearProject] `json:"projects"`
	}
	err := s.withToken(ctx, project, "linear", func(token string) error {
		return s.linear(ctx, token, `query CircularProjects($after: String) { projects(first: 100, after: $after) { nodes { id name } pageInfo { hasNextPage endCursor } } }`, cursorVariables(cursor), &data)
	})
	return data.Projects.public(), err
}

func cursorVariables(cursor string) map[string]any {
	if cursor == "" {
		return map[string]any{"after": nil}
	}
	return map[string]any{"after": cursor}
}

func (s *Service) LinearIssues(ctx context.Context, project, team, externalProject, cursor string) (CursorPage[LinearIssue], error) {
	teamID, err := uuid.Parse(team)
	if err != nil {
		return CursorPage[LinearIssue]{}, ErrAccess
	}
	// Only parsed UUIDs are interpolated. Pagination remains a GraphQL variable.
	filter := fmt.Sprintf(`team: {id: {eq: %q}}`, teamID.String())
	if externalProject != "" {
		id, err := uuid.Parse(externalProject)
		if err != nil {
			return CursorPage[LinearIssue]{}, ErrAccess
		}
		filter += fmt.Sprintf(`, project: {id: {eq: %q}}`, id.String())
	}
	query := `query CircularIssues($after: String) { issues(first: 50, after: $after, filter: {` + filter + `}) { nodes { id identifier title url state { name } } pageInfo { hasNextPage endCursor } } }`
	var data struct {
		Issues linearPage[LinearIssue] `json:"issues"`
	}
	err = s.withToken(ctx, project, "linear", func(token string) error { return s.linear(ctx, token, query, cursorVariables(cursor), &data) })
	return data.Issues.public(), err
}

func (s *Service) linearIssue(ctx context.Context, token, issue string) (LinearIssue, error) {
	id, err := uuid.Parse(issue)
	if err != nil {
		return LinearIssue{}, ErrAccess
	}
	var data struct {
		Issue *LinearIssue `json:"issue"`
	}
	err = s.linear(ctx, token, fmt.Sprintf(`query CircularIssue { issue(id: %q) { id identifier title description url state { name } } }`, id.String()), nil, &data)
	if err != nil {
		return LinearIssue{}, err
	}
	if data.Issue == nil || data.Issue.ID != id.String() {
		return LinearIssue{}, ErrAccess
	}
	return *data.Issue, nil
}
