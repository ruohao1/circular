package prreviews

import "github.com/google/uuid"

type Source struct {
	RunID         *uuid.UUID `json:"run_id,omitempty"`
	WorkSessionID *uuid.UUID `json:"work_session_id,omitempty"`
}

func (s Source) Validate() error {
	if (s.RunID == nil) == (s.WorkSessionID == nil) || s.RunID != nil && *s.RunID == uuid.Nil || s.WorkSessionID != nil && *s.WorkSessionID == uuid.Nil {
		return ErrSourceInvalid
	}
	return nil
}
func (s Source) Key() string {
	if s.Validate() != nil {
		return ""
	}
	if s.RunID != nil {
		return "run:" + s.RunID.String()
	}
	return "work:" + s.WorkSessionID.String()
}
func (s LaunchSnapshot) ReviewSource() Source {
	if s.Source != nil {
		return *s.Source
	}
	return Source{RunID: &s.SourceRunID}
}
func (s LaunchSnapshot) ValidateSource() error {
	if s.ReviewSource().Validate() != nil {
		return ErrSourceInvalid
	}
	if s.Source == nil && s.Version == 0 {
		return nil
	}
	if s.Version != 2 || s.Source == nil || s.Source.WorkSessionID == nil || s.SourceRunID != uuid.Nil {
		return ErrSourceInvalid
	}
	return nil
}
