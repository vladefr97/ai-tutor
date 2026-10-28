-- +goose Up
create table accounts (
    id uuid primary key default gen_random_uuid(),
    email text not null unique,
    is_teacher boolean not null default false,
    created_at timestamptz not null default now()
);

create table login_codes (
    id uuid primary key default gen_random_uuid(),
    account_id uuid not null references accounts on delete cascade,
    code_hash bytea not null,
    expires_at timestamptz not null,
    used_at timestamptz,
    failed_attempts int not null default 0,
    created_at timestamptz not null default now()
);
create index login_codes_account on login_codes (account_id, created_at desc);

create table sessions (
    token_hash bytea primary key,
    account_id uuid not null references accounts on delete cascade,
    expires_at timestamptz not null,
    created_at timestamptz not null default now()
);

create table courses (
    id uuid primary key default gen_random_uuid(),
    learner_id uuid not null references accounts on delete cascade,
    direction text not null check (direction in ('python', 'git', 'pm')),
    status text not null default 'interview',
    goal text,
    created_at timestamptz not null default now()
);
create index courses_learner on courses (learner_id, created_at);

-- +goose Down
drop table courses;
drop table sessions;
drop table login_codes;
drop table accounts;
