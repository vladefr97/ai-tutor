-- +goose Up
alter table courses add column profile jsonb;

-- One row per agent session (role) of a course; dir holds CODEX_HOME and workspace.
create table agent_sessions (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    role text not null,
    thread_id text,
    dir text not null,
    model text not null,
    created_at timestamptz not null default now()
);
create index agent_sessions_course on agent_sessions (course_id, role, created_at);

-- Interview and plan discussion with the Planner mentor.
create table messages (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    author text not null check (author in ('learner', 'mentor')),
    text text not null,
    created_at timestamptz not null default now()
);
create index messages_course on messages (course_id, created_at);

create table operations (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    kind text not null,
    status text not null default 'queued' check (status in ('queued', 'running', 'succeeded', 'failed')),
    stage text,
    input jsonb not null default '{}',
    error_code text,
    error_message text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);
-- At most one active operation per course.
create unique index operations_one_active on operations (course_id) where status in ('queued', 'running');

-- +goose Down
drop table operations;
drop table messages;
drop table agent_sessions;
alter table courses drop column profile;
