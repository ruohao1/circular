-- A stable preset identity distinguishes supplied Agents from user-created ones.
-- NULL leaves existing custom Agents unchanged and permits any number of them.
ALTER TABLE agents ADD COLUMN preset VARCHAR(100);
ALTER TABLE agents ADD CONSTRAINT agents_project_preset_key UNIQUE(project_id, preset);
