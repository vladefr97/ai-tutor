-- +goose Up
-- Chat with the Learning Mentor on the course page; separate from the interview.
create table chat_messages (
    id uuid primary key default gen_random_uuid(),
    course_id uuid not null references courses on delete cascade,
    author text not null check (author in ('learner', 'mentor')),
    text text not null,
    page_id text,
    created_at timestamptz not null default now()
);
create index chat_messages_course on chat_messages (course_id, created_at);

-- +goose Down
drop table chat_messages;
