-- +goose Up
create table visits (
    account_id uuid not null references accounts on delete cascade,
    at timestamptz not null
);
create index visits_at on visits (at, account_id);

-- +goose Down
drop table visits;
