-- +goose Up
-- Immutable plan versions; review is written once by the Plan reviewer.
create table plan_versions (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    number int not null,
    plan jsonb not null,
    explanation text not null,
    review jsonb,
    approved boolean not null default false,
    created_at timestamptz not null default now(),
    unique (course_id, number)
);

alter table courses add column confirmed_plan_version_id uuid references plan_versions;

-- +goose Down
alter table courses drop column confirmed_plan_version_id;
drop table plan_versions;
