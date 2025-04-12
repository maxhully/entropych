create table if not exists user (
    user_id integer primary key not null,
    name text not null,
    password_hash blob,
    password_salt blob
);

create table if not exists post (
    post_id integer primary key not null,
    user_id integer references user(user_id),
    created_at integer not null, /* unix timestamp */
    content text not null
);
create index if not exists post_user_id_idx on post (user_id);

