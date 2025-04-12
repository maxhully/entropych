/* All timestamp columns are unix timestamps in UTC */

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

create table if not exists user_session (
    user_session_id integer primary key not null,
    user_id integer references user(user_id),
    session_public_id blob not null, /* random string we can put in a cookie */
    created_at integer not null, /* unix timestamp */
    expiration_time integer not null /* unix timestamp */
);
create index if not exists user_session_session_public_id_idx on user_session (session_public_id);
