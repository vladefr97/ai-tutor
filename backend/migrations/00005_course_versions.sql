-- +goose Up
-- A course version becomes visible only when status = 'ready' and the course points to it.
create table course_versions (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    plan_version_id uuid not null references plan_versions,
    content_hash text not null,
    status text not null default 'uploading' check (status in ('uploading', 'ready')),
    created_at timestamptz not null default now()
);

alter table courses add column content_version_id uuid references course_versions;

-- +goose Down
alter table courses drop column content_version_id;
drop table course_versions;
