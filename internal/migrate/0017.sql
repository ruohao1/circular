ALTER TABLE tasks
    ADD COLUMN planned_start_date DATE,
    ADD COLUMN planned_finish_date DATE,
    ADD CONSTRAINT tasks_planned_start_date_check CHECK (
        planned_start_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'
    ),
    ADD CONSTRAINT tasks_planned_finish_date_check CHECK (
        planned_finish_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'
    ),
    ADD CONSTRAINT tasks_planned_date_range_check CHECK (
        planned_finish_date >= planned_start_date
    );

CREATE INDEX tasks_scheduled_idx ON tasks(project_id, planned_start_date, id)
    WHERE planned_start_date IS NOT NULL AND planned_finish_date IS NOT NULL;
CREATE INDEX tasks_unscheduled_idx ON tasks(project_id, created_at DESC, id DESC)
    WHERE planned_start_date IS NULL OR planned_finish_date IS NULL;
