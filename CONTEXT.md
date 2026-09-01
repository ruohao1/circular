# Circular Control Plane

Circular coordinates engineering work performed by replaceable coding-agent backends.

## Language

**Project**:
A planning scope that groups repositories, agents, and tasks.

**Repository**:
A source-code repository registered with a project.
_Avoid_: Repo

**Agent**:
A reusable, user-defined engineering specialization and backend configuration. It is not a running process or an execution attempt.
_Avoid_: Bot, worker, run

**Task**:
A durable unit of engineering work that may be attempted more than once.
_Avoid_: Job

**Run**:
One concrete execution attempt of a task by an agent using a selected backend.
_Avoid_: Agent, job

**Workspace**:
The isolated checkout and runtime allocation assigned to one run.
_Avoid_: Sandbox

**Event**:
An immutable, ordered fact recorded during a run for replay, audit, and live presentation.

**Approval**:
A recorded request for a person or policy to authorize a run action, and its eventual resolution.

**Artifact**:
A durable output produced by a run, such as a diff, log, report, or build result.

**Delegation**:
A platform-validated request from a parent run to create child work for a permitted target agent.
_Avoid_: Agent message

**Integration**:
A configured connection to an external system. External identities and references do not replace Circular domain entities.

**Backend**:
A replaceable adapter that drives an external coding-agent reasoning loop and emits backend events.
_Avoid_: Agent
