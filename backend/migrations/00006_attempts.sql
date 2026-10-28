-- +goose Up
-- One row per submitted answer. A grade is written only to its own attempt
-- while it is pending, so a late result never touches a newer attempt.
create table attempts (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    content_version_id uuid not null references course_versions,
    item_id text not null,
    item_type text not null,
    mode text not null default 'learning' check (mode in ('learning', 'exam')),
    answer jsonb not null,
    status text not null default 'pending' check (status in ('pending', 'passed', 'failed', 'error')),
    feedback text,
    hint text,
    explanation text,
    review jsonb,
    created_at timestamptz not null default now(),
    graded_at timestamptz
);
create index attempts_course_item on attempts (course_id, item_id, created_at);
create unique index attempts_one_pending on attempts (course_id, item_id) where status = 'pending';

-- +goose Down
drop table attempts;
