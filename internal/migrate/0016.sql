-- Planning belongs to durable Tasks; Run status remains independent.
ALTER TABLE projects ADD COLUMN task_board_revision BIGINT NOT NULL DEFAULT 1
    CHECK (task_board_revision BETWEEN 1 AND 9007199254740991);
ALTER TABLE agents ADD CONSTRAINT agents_project_id_id_key UNIQUE (project_id,id);
ALTER TABLE tasks
    ADD COLUMN priority VARCHAR(20) NOT NULL DEFAULT 'none'
        CHECK (priority IN ('none','low','normal','high','urgent')),
    ADD COLUMN preferred_agent_id UUID,
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    ADD COLUMN board_position BIGINT,
    ADD CONSTRAINT tasks_planning_status CHECK (status IN ('open','ready','in_progress','in_review','completed','cancelled')),
    ADD CONSTRAINT tasks_preferred_agent_scope FOREIGN KEY (project_id,preferred_agent_id)
        REFERENCES agents(project_id,id) ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED;

WITH positions AS (
    SELECT id,row_number() OVER (PARTITION BY project_id,status ORDER BY created_at DESC,id DESC)-1 AS position
    FROM tasks
) UPDATE tasks SET board_position=positions.position FROM positions WHERE tasks.id=positions.id;
ALTER TABLE tasks ALTER COLUMN board_position SET NOT NULL;
ALTER TABLE tasks ADD CONSTRAINT tasks_board_position CHECK (board_position >= 0);
CREATE INDEX tasks_planning_order ON tasks(project_id,status,board_position,id);

-- All existing creation paths participate, including imports and discovery.
-- ON CONFLICT no-op updates take the lock but do not change either revision.
CREATE FUNCTION place_new_task() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM id FROM projects WHERE id=NEW.project_id FOR NO KEY UPDATE;
    SELECT COALESCE(max(board_position),-1)+1 INTO NEW.board_position
        FROM tasks WHERE project_id=NEW.project_id AND status=NEW.status;
    RETURN NEW;
END $$;
CREATE TRIGGER task_planning_insert BEFORE INSERT ON tasks
    FOR EACH ROW EXECUTE FUNCTION place_new_task();
CREATE FUNCTION mark_task_created() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE projects SET task_board_revision=task_board_revision+1 WHERE id=NEW.project_id;
    RETURN NEW;
END $$;
CREATE TRIGGER task_planning_created AFTER INSERT ON tasks
    FOR EACH ROW EXECUTE FUNCTION mark_task_created();
